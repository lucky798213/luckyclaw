package sandbox

import (
	"strings"
	"testing"
)

func TestBuildContainerCreateArgsAppliesSecurityPolicy(t *testing.T) {
	policy := DockerPolicy{
		Image: "luckyclaw-sandbox:test", CPUs: 1, MemoryMB: 512, PIDsLimit: 128,
		TmpfsMB: 64, MaxOutputBytes: 1024, MaxFileBytes: 1024,
	}
	arguments := buildContainerCreateArgs(policy, "/tmp/workspace", []SkillMount{{Name: "runner", HostPath: "/tmp/runner"}}, 1000, 1000)
	joined := strings.Join(arguments, " ")
	for _, expected := range []string{
		"--read-only", "--cap-drop ALL", "--security-opt no-new-privileges",
		"--network none", "--cpus 1", "--memory 512m", "--memory-swap 512m",
		"--pids-limit 128", "--user 1000:1000", "target=/workspace",
		"target=/skills/runner,readonly", "luckyclaw-sandbox:test",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("arguments %q do not contain %q", joined, expected)
		}
	}
}

func TestLimitedBufferTruncatesWithoutBlockingWriter(t *testing.T) {
	buffer := &limitedBuffer{limit: 4}
	written, err := buffer.Write([]byte("abcdefgh"))
	if err != nil || written != 8 || buffer.buffer.String() != "abcd" || !buffer.truncated {
		t.Fatalf("limited buffer = %d, %v, %q, %v", written, err, buffer.buffer.String(), buffer.truncated)
	}
}

func TestBuildDockerExecArgsKeepsCommandOutOfHostShell(t *testing.T) {
	command := `echo "$(touch /tmp/host-must-not-run)" && printf '%s' "$HOME"`
	arguments := buildDockerExecArgs("container", "/tmp/marker", command)
	if arguments[len(arguments)-1] != command || arguments[0] != "exec" || arguments[1] != "container" {
		t.Fatalf("exec arguments = %#v", arguments)
	}
	if strings.Contains(arguments[5], command) {
		t.Fatal("command was interpolated into the shell script")
	}
}

func TestParseDirectoryEntriesSortsAndPreservesTypes(t *testing.T) {
	entries, err := parseDirectoryEntries([]byte("z.txt\x00f\x003\x00600\x00a\x00d\x004096\x00700\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "a" || entries[0].Type != "directory" || entries[1].Name != "z.txt" || entries[1].Size != 3 {
		t.Fatalf("entries = %+v", entries)
	}
	if _, err := parseDirectoryEntries([]byte("broken\x00f\x00")); err == nil {
		t.Fatal("malformed listing error = nil")
	}
}
