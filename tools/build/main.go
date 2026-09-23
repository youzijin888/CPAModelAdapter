// Build cross-platform release bundles. Requires Go only on the build machine.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func main() {
	must(os.MkdirAll("dist", 0755))
	checksums := ""
	for _, platform := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			name := "cpa-" + platform + "-" + arch
			exe := "cpa"
			installer := "install.sh"
			if platform == "windows" {
				exe += ".exe"
				installer = "install.ps1"
			}
			binary := filepath.Join("dist", name)
			if platform == "windows" {
				binary += ".exe"
			}
			tool := filepath.Join(runtime.GOROOT(), "bin", "go")
			if runtime.GOOS == "windows" {
				tool += ".exe"
			}
			cmd := exec.Command(tool, "build", "-trimpath", "-ldflags=-s -w", "-o", binary, "./cmd/cpa")
			cmd.Env = append(os.Environ(), "GOOS="+platform, "GOARCH="+arch, "CGO_ENABLED=0")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			must(cmd.Run())
			files := []struct {
				source, name string
				mode         int64
			}{{binary, exe, 0755}, {installer, installer, 0755}, {"README.md", "README.md", 0644}, {"THIRD_PARTY_NOTICES.md", "THIRD_PARTY_NOTICES.md", 0644},
				{"docs/licenses/CODEX-APACHE-2.0.txt", "docs/licenses/CODEX-APACHE-2.0.txt", 0644},
				{"docs/licenses/CODEX-NOTICE.txt", "docs/licenses/CODEX-NOTICE.txt", 0644},
				{"cmd/cpa/assets/README.md", "cmd/cpa/assets/README.md", 0644}}
			bundle := filepath.Join("dist", name+".tar.gz")
			if platform == "windows" {
				bundle = filepath.Join("dist", name+".zip")
			}
			out, err := os.Create(bundle)
			must(err)
			if platform == "windows" {
				z := zip.NewWriter(out)
				for _, f := range files {
					b, e := os.ReadFile(f.source)
					must(e)
					h := &zip.FileHeader{Name: f.name, Method: zip.Deflate}
					h.SetMode(os.FileMode(f.mode))
					w, e := z.CreateHeader(h)
					must(e)
					_, e = w.Write(b)
					must(e)
				}
				must(z.Close())
			} else {
				gz := gzip.NewWriter(out)
				tw := tar.NewWriter(gz)
				for _, f := range files {
					b, e := os.ReadFile(f.source)
					must(e)
					must(tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(b))}))
					_, e = tw.Write(b)
					must(e)
				}
				must(tw.Close())
				must(gz.Close())
			}
			must(out.Close())
			for _, p := range []string{binary, bundle} {
				f, e := os.Open(p)
				must(e)
				h := sha256.New()
				_, e = io.Copy(h, f)
				must(e)
				must(f.Close())
				checksums += fmt.Sprintf("%x  %s\n", h.Sum(nil), filepath.Base(p))
			}
			fmt.Println("Built", bundle)
		}
	}
	must(os.WriteFile(filepath.Join("dist", "SHA256SUMS"), []byte(checksums), 0644))
}
