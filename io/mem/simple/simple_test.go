package simple

import (
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"log"
	"testing"

	jsfs "github.com/gopherfs/fs"
	"github.com/kylelemons/godebug/pretty"
)

//go:embed simple.go pearson.go
var FSM embed.FS

func mustRead(fsys fs.FS, name string) []byte {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		panic(err)
	}
	return b
}

func md5Sum(b []byte) string {
	h := md5.New()
	h.Write(mustRead(FSM, "simple.go"))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestMerge(t *testing.T) {
	mem := New(WithPearson())
	mem.WriteFile("/where/the/streets/have/no/name/u2.txt", []byte("joshua tree"), 0660)

	if err := jsfs.Merge(mem, FSM, "/songs/"); err != nil {
		panic(err)
	}
	mem.RO()

	if err := mem.WriteFile("/some/file", []byte("who cares"), 0660); err == nil {
		t.Fatalf("TestMerge(write after .RO()): should not be able to write, but did")
	}

	pathsToCheck := []string{
		"songs",
		"where",
		"where/the",
		"where/the/streets",
		"where/the/streets/have",
		"where/the/streets/have/no",
		"where/the/streets/have/no/name",
	}

	for _, p := range pathsToCheck {
		fi, err := fs.Stat(mem, p)
		if err != nil {
			t.Fatalf("TestMerge(stat dir): (%s) err: %s", p, err)
		}
		if !fi.IsDir() {
			t.Fatalf("TestMerge(fi.IsDir): (%s) was false", p)
		}
	}

	fs.WalkDir(mem, ".",
		func(path string, d fs.DirEntry, err error) error {
			log.Println("simple walk: ", path)
			return nil
		},
	)

	b, err := mem.ReadFile("where/the/streets/have/no/name/u2.txt")
	if err != nil {
		t.Fatalf("TestMerge(simple.ReadFile): expected file gave error: %s", err)
	}
	if bytes.Compare(b, []byte("joshua tree")) != 0 {
		t.Fatalf("TestMerge(simple.ReadFile): -want/+got:\n%s", pretty.Compare("joshua tree", string(b)))
	}

	if md5Sum(mustRead(mem, "songs/simple.go")) != md5Sum(mustRead(FSM, "simple.go")) {
		t.Fatalf("TestMerge(md5 check on simple.go): got %q, want %q", md5Sum(mustRead(mem, "songs/simple.go")), md5Sum(mustRead(FSM, "simple.go")))
	}
	if md5Sum(mustRead(mem, "songs/pearson.go")) != md5Sum(mustRead(FSM, "pearson.go")) {
		t.Fatalf("TestMerge(md5 check on pearson.go): got %q, want %q", md5Sum(mustRead(mem, "songs/pearson.go")), md5Sum(mustRead(FSM, "pearson.go")))
	}
}

func TestTransform(t *testing.T) {
	transformer := func(name string, content []byte) ([]byte, error) {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, err := zw.Write(content)
		if err != nil {
			return nil, err
		}
		if err := zw.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}

	mem := New()
	if err := jsfs.Merge(mem, FSM, "", jsfs.WithTransform(transformer)); err != nil {
		panic(err)
	}
	mem.RO()

	reader, err := mem.Open("simple.go")
	if err != nil {
		t.Fatalf("TestTransform: destination did not have simple.go: %s", err)
	}
	zr, err := gzip.NewReader(reader)
	if err != nil {
		t.Fatalf("TestTranform: unexpected problem reading gzip simple.go: %s", err)
	}
	out := bytes.Buffer{}
	if _, err := io.Copy(&out, zr); err != nil {
		t.Fatalf("TestTranform: unexpected problem during io.Copy(): %s", err)
	}
	if err := zr.Close(); err != nil {
		t.Fatalf("TestTranform: unexpected problem during gzip close: %s", err)
	}
	want, err := FSM.ReadFile("simple.go")
	if err != nil {
		panic("simple.go not in embedded file system")
	}
	got := out.Bytes()
	if diff := pretty.Compare(string(want), string(got)); diff != "" {
		t.Fatalf("TestTransform: -want/+got:\n%s", diff)
	}
}

func TestStat(t *testing.T) {
	systems := []*FS{}

	mem := New()
	mem.WriteFile("/some/dir/file.txt", []byte("joshua tree"), 0660)
	systems = append(systems, mem)

	mem = New(WithPearson())
	mem.WriteFile("/some/dir/file.txt", []byte("joshua tree"), 0660)
	mem.RO()
	systems = append(systems, mem)

	for _, system := range systems {
		stat, err := system.Stat("/some/dir")
		if err != nil {
			t.Fatalf("TestStat: could not Stat the dir: %s", err)
		}
		if !stat.IsDir() {
			t.Fatalf("TestStat: dir did not show as IsDir()")
		}
	}
}

func TestSeek(t *testing.T) {
	f := &file{content: []byte("hello world")}

	_, err := f.Seek(1, io.SeekStart)
	if err != nil {
		t.Fatalf("TestSeek(f.Seek(1, io.SeekStart)): got err == %s, want err == nil", err)
	}

	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("TestSeek(on read after SeekStart): got err == %s, want err == nil", err)
	}
	if string(b) != "ello world" {
		t.Fatalf("TestSeek: got string %q, want 'ello world'", string(b))
	}

	_, err = f.Seek(-2, io.SeekEnd)
	if err != nil {
		t.Fatalf("TestSeek(f.Seek(2, io.SeekEnd)): got err == %s, want err == nil", err)
	}
	b, err = io.ReadAll(f)
	if err != nil {
		t.Fatalf("TestSeek(on read after SeekEnd)): got err == %s, want err == nil", err)
	}
	if string(b) != "ld" {
		t.Fatalf("TestSeek: got string %q, want 'ld'", string(b))
	}
	f.Seek(5, io.SeekStart)

	_, err = f.Seek(-2, io.SeekCurrent)
	if err != nil {
		t.Fatalf("TestSeek(f.Seek(-2, io.SeekCurrent)): got err == %s, want err == nil", err)
	}
	b, err = io.ReadAll(f)
	if err != nil {
		t.Fatalf("TestSeek(on read after SeekCurrent)): got err == %s, want err == nil", err)
	}
	if string(b) != "lo world" {
		t.Fatalf("TestSeek: got string %q, want 'lo world'", string(b))
	}
}
