package llblib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithContentHash(t *testing.T) {
	// Create a temporary directory with deterministic test files
	tmpDir := t.TempDir()

	// Fixed timestamp for deterministic testing
	fixedTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	testFile1 := filepath.Join(tmpDir, "test1.txt")
	testFile2 := filepath.Join(tmpDir, "test2.txt")

	// Create files with known content
	require.NoError(t, os.WriteFile(testFile1, []byte("Hello World"), 0644))
	require.NoError(t, os.WriteFile(testFile2, []byte("Another file"), 0644))

	// Set deterministic timestamps
	require.NoError(t, os.Chtimes(testFile1, fixedTime, fixedTime))
	require.NoError(t, os.Chtimes(testFile2, fixedTime, fixedTime))
	require.NoError(t, os.Chtimes(tmpDir, fixedTime, fixedTime))

	// Test without content hash (modification time based)
	id1, err := localUniqueID(tmpDir)
	require.NoError(t, err)

	// Test with content hash - should be deterministic
	id2, err := localUniqueID(tmpDir, WithContentHash)
	require.NoError(t, err)

	// The content hash should be exactly this value for our known test data
	// This hash is calculated from:
	// - "test1.txt" + mode(0644) + "Hello World" (first 64KB)
	// - "test2.txt" + mode(0644) + "Another file" (first 64KB)
	// Using xxhash algorithm: 1cd8cdf4b1f1721e
	expectedHash := "1cd8cdf4b1f1721e"
	assert.Contains(t, id2, expectedHash)

	// Verify it's different from modification time based ID
	assert.NotEqual(t, id1, id2)

	// Test deterministic behavior - should get same hash again
	id3, err := localUniqueID(tmpDir, WithContentHash)
	require.NoError(t, err)
	assert.Equal(t, id2, id3)
}

func TestWithContentHashKnownValues(t *testing.T) {
	// Test with completely known, controlled data
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "known.txt")

	// Write exactly "test" - 4 bytes
	require.NoError(t, os.WriteFile(testFile, []byte("test"), 0644))

	fixedTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(testFile, fixedTime, fixedTime))

	// Test single file content hash
	id, err := localUniqueID(testFile, WithContentHash)
	require.NoError(t, err)

	// For a single file with content "test" and mode 0644, the xxhash should be deterministic
	// xxhash64(mode(0644) + "test") = f12072e5a031e9d7
	// This ensures our implementation matches expected xxhash behavior
	expectedSingleFileHash := "f12072e5a031e9d7"
	assert.Contains(t, id, expectedSingleFileHash)

	// Test that the same content always produces the same hash
	id2, err := localUniqueID(testFile, WithContentHash)
	require.NoError(t, err)
	assert.Equal(t, id, id2)
}

func TestWithContentHashChangeDetection(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "change.txt")

	fixedTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	// Create file with initial content
	require.NoError(t, os.WriteFile(testFile, []byte("initial"), 0644))
	require.NoError(t, os.Chtimes(testFile, fixedTime, fixedTime))

	id1, err := localUniqueID(testFile, WithContentHash)
	require.NoError(t, err)

	// Change content but keep same timestamp
	require.NoError(t, os.WriteFile(testFile, []byte("changed"), 0644))
	require.NoError(t, os.Chtimes(testFile, fixedTime, fixedTime)) // Same timestamp!

	id2, err := localUniqueID(testFile, WithContentHash)
	require.NoError(t, err)

	// Content hash should detect the change even with same timestamp
	assert.NotEqual(t, id1, id2)
	// Both should contain hex hash values (no prefixes)
	assert.Regexp(t, `[0-9a-f]{16}`, id1)
	assert.Regexp(t, `[0-9a-f]{16}`, id2)

	// But modification time based hash would miss this change
	id3, err := localUniqueID(testFile) // no WithContentHash()
	require.NoError(t, err)

	id4, err := localUniqueID(testFile) // no WithContentHash()
	require.NoError(t, err)

	// Modification time based should be same (same timestamp)
	assert.Equal(t, id3, id4)
}

func TestWithContentHashFileMode(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "mode_test.txt")

	fixedTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	// Create file with initial permissions 0644
	require.NoError(t, os.WriteFile(testFile, []byte("same content"), 0644))
	require.NoError(t, os.Chtimes(testFile, fixedTime, fixedTime))

	id1, err := localUniqueID(testFile, WithContentHash)
	require.NoError(t, err)

	// Change permissions to 0755 but keep same content and timestamp
	require.NoError(t, os.Chmod(testFile, 0755))
	require.NoError(t, os.Chtimes(testFile, fixedTime, fixedTime)) // Same timestamp!

	id2, err := localUniqueID(testFile, WithContentHash)
	require.NoError(t, err)

	// Content hash should detect the permission change
	assert.NotEqual(t, id1, id2)
	assert.Regexp(t, `[0-9a-f]{16}`, id1)
	assert.Regexp(t, `[0-9a-f]{16}`, id2)
}

func TestWithContentHashLargeFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a large file (200KB) to test multi-chunk hashing
	largeFile := filepath.Join(tmpDir, "large.txt")

	// Create content larger than 128KB (2 * 64KB) to trigger middle+last chunk reading
	var content strings.Builder
	for range 3000 {
		content.WriteString("This is line ")
		content.WriteString(strings.Repeat("0123456789", 7)) // 70 chars per line
		content.WriteString("\n")
	}
	largeContent := content.String()

	fixedTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	require.NoError(t, os.WriteFile(largeFile, []byte(largeContent), 0644))
	require.NoError(t, os.Chtimes(largeFile, fixedTime, fixedTime))

	// Get initial hash
	id1, err := localUniqueID(largeFile, WithContentHash)
	require.NoError(t, err)

	// Append to end (should be detected by last chunk reading)
	appendedContent := largeContent + "APPENDED_DATA_AT_END"
	require.NoError(t, os.WriteFile(largeFile, []byte(appendedContent), 0644))
	require.NoError(t, os.Chtimes(largeFile, fixedTime, fixedTime)) // Same timestamp!

	id2, err := localUniqueID(largeFile, WithContentHash)
	require.NoError(t, err)

	// Should detect the append
	assert.NotEqual(t, id1, id2, "Content hash should detect appended data")

	// Test middle modification
	modifiedContent := largeContent[:len(largeContent)/2] + "MODIFIED_MIDDLE" + largeContent[len(largeContent)/2+14:]
	require.NoError(t, os.WriteFile(largeFile, []byte(modifiedContent), 0644))
	require.NoError(t, os.Chtimes(largeFile, fixedTime, fixedTime))

	id3, err := localUniqueID(largeFile, WithContentHash)
	require.NoError(t, err)

	// Should detect the middle modification
	assert.NotEqual(t, id1, id3, "Content hash should detect middle modification")
	assert.NotEqual(t, id2, id3, "All modifications should produce different hashes")

	t.Logf("Original hash: %s", id1)
	t.Logf("Appended hash: %s", id2)
	t.Logf("Modified hash: %s", id3)
}

func TestWithContentHashOrderConsistency(t *testing.T) {
	// Test that fsutil.Walk order is deterministic regardless of file creation order
	fixedTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	
	var hashes []string
	
	// Create the same files in different orders multiple times
	for iteration := 0; iteration < 5; iteration++ {
		tmpDir := t.TempDir()
		
		// Files to create (content = filename without extension, different creation order each time)
		files := []string{"zebra.txt", "alpha.txt", "beta.txt", "gamma.txt", "delta.txt"}
		
		// Shuffle the order of file creation for each iteration
		for i := len(files) - 1; i > 0; i-- {
			j := (iteration*7 + i*3) % (i + 1) // Deterministic but different shuffle each iteration
			files[i], files[j] = files[j], files[i]
		}
		
		// Create files in this shuffled order
		for _, filename := range files {
			filePath := filepath.Join(tmpDir, filename)
			// Content is just the filename without .txt extension
			content := strings.TrimSuffix(filename, ".txt")
			require.NoError(t, os.WriteFile(filePath, []byte(content), 0644))
			require.NoError(t, os.Chtimes(filePath, fixedTime, fixedTime))
		}
		require.NoError(t, os.Chtimes(tmpDir, fixedTime, fixedTime))
		
		// Get content hash
		hash, err := localUniqueID(tmpDir, WithContentHash)
		require.NoError(t, err)
		
		hashes = append(hashes, hash)
		t.Logf("Iteration %d (creation order: %v): %s", iteration+1, files, hash)
	}
	
	// Extract just the content hash portion (after the last comma) for comparison
	var contentHashes []string
	for _, fullHash := range hashes {
		parts := strings.Split(fullHash, ",")
		contentHash := parts[len(parts)-1] // Get the part after the last comma
		contentHashes = append(contentHashes, contentHash)
	}
	
	// All content hashes should be identical regardless of file creation order
	for i := 1; i < len(contentHashes); i++ {
		assert.Equal(t, contentHashes[0], contentHashes[i], 
			"Content hash should be deterministic regardless of file creation order (iteration %d vs 1)", i+1)
	}
	
	t.Logf("Content hash (consistent): %s", contentHashes[0])
}

func TestWithTracedContentHash(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "trace_test.txt")
	
	fixedTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	
	require.NoError(t, os.WriteFile(testFile, []byte("traced content"), 0644))
	require.NoError(t, os.Chtimes(testFile, fixedTime, fixedTime))
	
	// Test with tracing
	var trace strings.Builder
	id, err := localUniqueID(testFile, WithTracedContentHash(&trace))
	require.NoError(t, err)
	
	// Verify trace contains expected information
	traceOutput := trace.String()
	assert.Contains(t, traceOutput, "Content Hash Trace for", "Trace should contain header")
	assert.Contains(t, traceOutput, "trace_test.txt", "Trace should contain filename")
	assert.Contains(t, traceOutput, "mode:", "Trace should contain file mode")
	assert.Contains(t, traceOutput, "Size:", "Trace should contain file size")
	assert.Contains(t, traceOutput, "Final hash:", "Trace should contain final hash")
	assert.Contains(t, traceOutput, "End Trace", "Trace should contain footer")
	
	// Verify hash is still valid
	assert.Regexp(t, `[0-9a-f]{16}`, id, "Should contain valid content hash")
	
	t.Logf("Trace output:\n%s", traceOutput)
	t.Logf("Hash with tracing: %s", id)
}
