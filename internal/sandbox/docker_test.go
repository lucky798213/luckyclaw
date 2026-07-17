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
