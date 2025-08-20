package llblib_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coryb/llblib"
	"github.com/moby/buildkit/client/llb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSolverLocalWithContentHash(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, withTimeout(30*time.Second))

	// Create a test directory with known content
	tmpDir := t.TempDir()
	testFile1 := filepath.Join(tmpDir, "test1.go")
	testFile2 := filepath.Join(tmpDir, "test2.txt")

	fixedTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	require.NoError(t, os.WriteFile(testFile1, []byte("package main\n\nfunc main() {}\n"), 0644))
	require.NoError(t, os.WriteFile(testFile2, []byte("ignored file"), 0644))

	// Set deterministic timestamps
	require.NoError(t, os.Chtimes(testFile1, fixedTime, fixedTime))
	require.NoError(t, os.Chtimes(testFile2, fixedTime, fixedTime))
	require.NoError(t, os.Chtimes(tmpDir, fixedTime, fixedTime))

	// Test content hash local
	contentHashLocal := r.Solver.Local(tmpDir, llb.IncludePatterns([]string{"*.go"}), llblib.WithContentHash)

	sess := r.Session(t)
	ctx := llblib.WithSession(r.Context, sess)

	// Convert to YAML to inspect the actual unique ID
	contentHashNode, err := llblib.ToYAML(ctx, contentHashLocal)
	require.NoError(t, err)

	// Convert to YAML bytes to check content
	yamlBytes, err := yaml.Marshal(contentHashNode)
	require.NoError(t, err)
	yamlStr := string(yamlBytes)

	// Verify that content hashing is working:
	// 1. The unique ID should be a SHA256 digest (not our sentinel value)
	// 2. It should not contain the sentinel value
	// 3. It should contain the include pattern

	assert.Contains(t, yamlStr, "local.unique: 'sha256:", "YAML should contain SHA256 unique ID")
	assert.NotContains(t, yamlStr, llblib.ContentHashSentinel, "YAML should not contain sentinel value")

	// Verify it contains the include pattern
	assert.Contains(t, yamlStr, `local.includepattern: '["*.go"]'`, "YAML should contain the include pattern")

	// Verify it's a local source
	assert.Contains(t, yamlStr, "source: local://", "YAML should indicate local source")

	t.Logf("Generated YAML:\n%s", yamlStr)
}
