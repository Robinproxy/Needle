#!/usr/bin/env python3
"""Reject damaged/incomplete/unsafe actual archives before executing binaries."""
import hashlib,io,json,pathlib,subprocess,tarfile,tempfile,unittest
VERIFY=pathlib.Path(__file__).with_name('verify-release.py')
class ArtifactTests(unittest.TestCase):
    def check_rejected(self,entries,expected):
        with tempfile.TemporaryDirectory() as temp:
            archive=pathlib.Path(temp)/'needle-linux-amd64.tar.gz'
            with tarfile.open(archive,'w:gz') as tf:
                for name,body,symlink in entries:
                    entry=tarfile.TarInfo(name)
                    if symlink:entry.type=tarfile.SYMTYPE;entry.linkname=symlink;tf.addfile(entry)
                    else:entry.size=len(body);tf.addfile(entry,io.BytesIO(body))
            pathlib.Path(str(archive)+'.sha256').write_text(hashlib.sha256(archive.read_bytes()).hexdigest()+'  '+archive.name)
            result=subprocess.run(['python3',str(VERIFY),str(archive),'--version','candidate','--commit','a'*40],capture_output=True,text=True)
            self.assertNotEqual(result.returncode,0);self.assertIn(expected,result.stderr)
    def test_symlink_rejected(self):self.check_rejected([('needle-agent',b'', '/bin/true')],'unsafe archive entry')
    def test_traversal_rejected(self):self.check_rejected([('../needle-agent',b'',None)],'unsafe archive entry')
    def test_duplicate_rejected(self):self.check_rejected([('needle-agent',b'',None),('needle-agent',b'',None)],'duplicate archive entries')
    def test_missing_server_rejected(self):
        manifest=json.dumps({'version':'candidate','commit':'a'*40}).encode()
        self.check_rejected([('build-manifest.json',manifest,None)],'required packaged component missing')
if __name__=='__main__':unittest.main()
