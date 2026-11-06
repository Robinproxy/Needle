package scripts_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Registry safety depends on workflow wiring as well as the shell guard: the
// lock must span the head check and write, without canceling version builds.
func TestDockerLatestPromotionIsolation(t *testing.T) {
	var workflow struct {
		Concurrency any `yaml:"concurrency"`
		Jobs        map[string]struct {
			If          string `yaml:"if"`
			Needs       string `yaml:"needs"`
			Concurrency struct {
				Group            string `yaml:"group"`
				CancelInProgress bool   `yaml:"cancel-in-progress"`
				Queue            string `yaml:"queue"`
			} `yaml:"concurrency"`
			Outputs map[string]string `yaml:"outputs"`
			Steps   []struct {
				Uses string            `yaml:"uses"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	data, err := os.ReadFile("../.github/workflows/docker.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	build, hasBuild := workflow.Jobs["build"]
	latest, hasLatest := workflow.Jobs["latest"]
	if !hasBuild || !hasLatest || workflow.Concurrency != nil || build.Concurrency.Group != "" {
		t.Fatal("version builds must not share a cancelable publication queue")
	}
	if latest.Needs != "build" || latest.If != "github.ref == 'refs/heads/main'" {
		t.Fatal("only successful main builds may attempt latest promotion")
	}
	// Keep the identity stable across revisions: old reruns must use this lock.
	if latest.Concurrency.Group != "needle-docker-latest" || latest.Concurrency.CancelInProgress || latest.Concurrency.Queue != "max" {
		t.Fatal("latest requires a shared noncanceling lock retaining pending jobs")
	}
	if build.Outputs["image_ref"] != "${{ steps.publish.outputs.image_ref }}" {
		t.Fatal("promotion must consume the exact image published by the build job")
	}
	for _, step := range build.Steps {
		if strings.Contains(step.Run, ":latest") || strings.Contains(step.Run, "--all-tags") || strings.Contains(step.Run, "promote-docker-latest") {
			t.Fatal("build jobs must not write latest or push unrelated local tags")
		}
	}
	foundGuard, currentPolicy := false, false
	for _, step := range latest.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") && step.With["ref"] == "main" {
			currentPolicy = true
		}
		if strings.Contains(step.Run, "scripts/promote-docker-latest.sh") {
			foundGuard = step.Env["SOURCE_COMMIT"] == "${{ github.sha }}" &&
				step.Env["SOURCE_IMAGE"] == "${{ needs.build.outputs.image_ref }}" &&
				strings.Contains(step.Run, `"$SOURCE_COMMIT" "$SOURCE_IMAGE"`)
		}
	}
	if !currentPolicy || !foundGuard {
		t.Fatal("locked promotion must use the current guard, event commit and tested digest")
	}
}
