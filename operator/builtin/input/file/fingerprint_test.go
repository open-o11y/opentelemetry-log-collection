// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package file

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewFingerprintDoesNotModifyOffset(t *testing.T) {
	fingerprint := "this is the fingerprint"
	next := "this comes after the fingerprint and is substantially longer than the fingerprint"
	extra := "fin"

	fileContents := fmt.Sprintf("%s%s%s\n", fingerprint, next, extra)

	f, _, tempDir := newTestFileOperator(t, nil, nil)
	f.fingerprintSize = len(fingerprint)

	// Create a new file
	temp := openTemp(t, tempDir)
	writeString(t, temp, fileContents)

	// Validate that the file is actually the expected size after writing
	info, err := temp.Stat()
	require.NoError(t, err)
	require.Equal(t, len(fileContents), int(info.Size()))

	// Set the file descriptors pointer to the beginning of the file
	temp.Seek(0, 0)

	fp, err := f.NewFingerprint(temp)
	require.NoError(t, err)

	// Validate the fingerprint is the correct size
	require.Equal(t, len(fingerprint), len(fp.FirstBytes))

	// Validate that reading the fingerprint did not adjust the
	// file descriptor's internal offset (as using Seek does)
	allButExtra := make([]byte, len(fingerprint)+len(next))
	n, err := temp.Read(allButExtra)
	require.NoError(t, err)
	require.Equal(t, len(allButExtra), n)
	require.Equal(t, fileContents[:len(allButExtra)], string(allButExtra))
}

func TestNewFingerprint(t *testing.T) {
	cases := []struct {
		name            string
		fingerprintSize int
		fileSize        int
		expectedLen     int
	}{
		{
			name:            "defaultExactFileSize",
			fingerprintSize: defaultFingerprintSize,
			fileSize:        defaultFingerprintSize,
			expectedLen:     defaultFingerprintSize,
		},
		{
			name:            "defaultWithFileHalfOfFingerprint",
			fingerprintSize: defaultFingerprintSize,
			fileSize:        defaultFingerprintSize / 2,
			expectedLen:     defaultFingerprintSize / 2,
		},
		{
			name:            "defaultWithFileTwiceFingerprint",
			fingerprintSize: defaultFingerprintSize,
			fileSize:        defaultFingerprintSize * 2,
			expectedLen:     defaultFingerprintSize,
		},
		{
			name:            "minFingerprintExactFileSize",
			fingerprintSize: minFingerprintSize,
			fileSize:        minFingerprintSize,
			expectedLen:     minFingerprintSize,
		},
		{
			name:            "minFingerprintWithSmallerFileSize",
			fingerprintSize: minFingerprintSize,
			fileSize:        minFingerprintSize / 2,
			expectedLen:     minFingerprintSize / 2,
		},
		{
			name:            "minFingerprintWithLargerFileSize",
			fingerprintSize: minFingerprintSize,
			fileSize:        defaultFingerprintSize,
			expectedLen:     minFingerprintSize,
		},
		{
			name:            "largeFingerprintSmallFile",
			fingerprintSize: 1024 * 1024,
			fileSize:        1024,
			expectedLen:     1024,
		},
		{
			name:            "largeFingerprintLargeFile",
			fingerprintSize: 1024 * 8,
			fileSize:        1024 * 128,
			expectedLen:     1024 * 8,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, _, tempDir := newTestFileOperator(t, nil, nil)
			f.fingerprintSize = tc.fingerprintSize

			// Create a new file
			temp := openTemp(t, tempDir)
			writeString(t, temp, stringWithLength(tc.fileSize))

			// Validate that the file is actually the expected size after writing
			info, err := temp.Stat()
			require.NoError(t, err)
			require.Equal(t, tc.fileSize, int(info.Size()))

			fp, err := f.NewFingerprint(temp)
			require.NoError(t, err)

			require.Equal(t, tc.expectedLen, len(fp.FirstBytes))
		})
	}
}

func TestFingerprintCopy(t *testing.T) {
	t.Parallel()
	cases := []string{
		"",
		"hello",
		"asdfsfaddsfas",
		stringWithLength(minFingerprintSize),
		stringWithLength(defaultFingerprintSize),
		stringWithLength(1234),
	}

	for _, tc := range cases {
		fp := &Fingerprint{FirstBytes: []byte(tc)}

		copy := fp.Copy()

		// Did not change original
		require.Equal(t, tc, string(fp.FirstBytes))

		// Copy is also good
		require.Equal(t, tc, string(copy.FirstBytes))

		// Modify copy
		copy.FirstBytes = append(copy.FirstBytes, []byte("also")...)

		// Still did not change original
		require.Equal(t, tc, string(fp.FirstBytes))

		// Copy is modified
		require.Equal(t, tc+"also", string(copy.FirstBytes))
	}
}

func TestFingerprintStartsWith(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
	}{
		{
			name: "same",
			a:    "hello",
			b:    "hello",
		},
		{
			name: "aStartsWithB",
			a:    "helloworld",
			b:    "hello",
		},
		{
			name: "bStartsWithA",
			a:    "hello",
			b:    "helloworld",
		},
		{
			name: "neither",
			a:    "hello",
			b:    "world",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fa := &Fingerprint{FirstBytes: []byte(tc.a)}
			fb := &Fingerprint{FirstBytes: []byte(tc.b)}

			require.Equal(t, strings.HasPrefix(tc.a, tc.b), fa.StartsWith(fb))
			require.Equal(t, strings.HasPrefix(tc.b, tc.a), fb.StartsWith(fa))
		})
	}
}

// Generates a file filled with many random bytes, then
// writes the same bytes to a second file, one byte at a time.
// Validates, after each byte is written, that fingerprint
// matching would successfully associate the two files.
// The static file can be thought of as the present state of
// the file, while each iteration of the growing file represents
// a possible state of the same file at a previous time.
func TestFingerprintStartsWith_FromFile(t *testing.T) {
	r := rand.New(rand.NewSource(112358))

	operator, _, tempDir := newTestFileOperator(t, nil, nil)
	operator.fingerprintSize *= 10

	fileLength := 12 * operator.fingerprintSize

	// Make a []byte we can write one at a time
	content := make([]byte, fileLength)
	r.Read(content) // Fill slice with random bytes

	// Overwrite some bytes with \n to ensure
	// we are testing a file with multiple lines
	newlineMask := make([]byte, fileLength)
	r.Read(newlineMask) // Fill slice with random bytes
	for i, b := range newlineMask {
		if b == 0 && i != 0 { // 1/256 chance, but never first byte
			content[i] = byte('\n')
		}
	}

	fullFile, err := ioutil.TempFile(tempDir, "")
	require.NoError(t, err)
	_, err = fullFile.Write(content)
	require.NoError(t, err)

	fff, err := operator.NewFingerprint(fullFile)
	require.NoError(t, err)

	partialFile, err := ioutil.TempFile(tempDir, "")
	require.NoError(t, err)

	// Write one byte at a time and validate that updated
	// full fingerprint still starts with partial
	for i := range content {
		_, err = partialFile.Write(content[i:i])
		require.NoError(t, err)

		pff, err := operator.NewFingerprint(fullFile)
		require.NoError(t, err)

		require.True(t, fff.StartsWith(pff))
	}
}

// TODO TestConfig (config_test.go) - sets defaults, errors appropriately, etc
