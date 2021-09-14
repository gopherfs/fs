package redis

import (
	"testing"

	"github.com/kylelemons/godebug/pretty"
)

func TestRedis(t *testing.T) {
	const testFile = "path/to/test/file"
	const testContent = "content"

	args := Args{
		Addr: "127.0.0.1:6379",
	}

	redisFS, err := New(args)
	if err != nil {
		panic(err)
	}

	if err := redisFS.Remove(testFile); err != nil {
		panic(err)
	}

	if err := redisFS.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatalf("TestRedis(WriteFile): got err == %s, want err == nil", err)
	}

	want, err := redisFS.ReadFile(testFile)
	if err != nil {
		t.Fatalf("TestRedis(ReadFile): got err == %s, want err == nil", err)
	}

	if err := redisFS.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatalf("TestRedis(WriteFile on existing file): got err == %s, want err == nil", err)
	}

	if diff := pretty.Compare(string(want), testContent); diff != "" {
		t.Fatalf("TestRedis(ReadFile): -want/+got:\n%s", diff)
	}
}
