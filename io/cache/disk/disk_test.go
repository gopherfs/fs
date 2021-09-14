package disk

import (
	"testing"
	"time"

	"github.com/kylelemons/godebug/pretty"
)

func TestFS(t *testing.T) {
	files := []string{
		"myfile/is/here",
		"my.jpg",
	}
	const testContent = "content"

	diskFS, err := New(
		"",
		WithExpireCheck(8*time.Second),
		WithExpireFiles(5*time.Second),
	)
	if err != nil {
		t.Fatalf("TestFS: got err == %s, want err == nil", err)
	}

	/*
		defer func() {
			fi, err := os.Stat(diskFS.Location())
			if err != nil {
				panic(err)
			}
			if !fi.IsDir() {
				panic("not dir")
			}
			dirEntries, err := os.ReadDir(diskFS.Location())
			if err != nil {
				panic(err)
			}
			if len(dirEntries) > len(files) {
				panic("trying to delete non-tempdir somehow")
			}
			if err := os.RemoveAll(diskFS.Location()); err != nil {
				panic(err)
			}
		}()
	*/

	for _, file := range files {
		if err := diskFS.WriteFile(file, []byte(testContent), 0644); err != nil {
			t.Fatalf("TestFS(WriteFile): got err == %s, want err == nil", err)
		}
	}

	for _, file := range files {
		want, err := diskFS.ReadFile(file)
		if err != nil {
			t.Fatalf("TestFS(ReadFile %s): got err == %s, want err == nil", file, err)
		}
		if diff := pretty.Compare(string(want), testContent); diff != "" {
			t.Fatalf("TestFS(ReadFile %q): -want/+got:\n%s", file, diff)
		}
	}

	for _, file := range files {
		if err := diskFS.WriteFile(file, []byte(testContent), 0644); err != nil {
			t.Errorf("TestFS(WriteFile on existing file): got err == %s, want err == nil", err)
		}
	}

	time.Sleep(10 * time.Second)
	for _, file := range files {
		if _, err := diskFS.Stat(file); err == nil {
			t.Errorf("TestFS(file expiration): found file(%s) that should have expired", file)
		}
	}

}
