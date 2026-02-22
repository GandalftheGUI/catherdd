package proto_test

import (
	"bytes"
	"testing"

	"github.com/ianremillard/grove/internal/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteReadFrameRoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		frameType byte
		payload   []byte
	}{
		{"data with payload", proto.AttachFrameData, []byte("hello world")},
		{"resize payload", proto.AttachFrameResize, []byte{0, 80, 0, 24}},
		{"detach no payload", proto.AttachFrameDetach, nil},
		{"data empty payload", proto.AttachFrameData, []byte{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := proto.WriteFrame(&buf, tc.frameType, tc.payload)
			require.NoError(t, err)

			ft, payload, err := proto.ReadFrame(&buf)
			require.NoError(t, err)
			assert.Equal(t, tc.frameType, ft)
			// Both nil and empty slice represent "no payload".
			if len(tc.payload) == 0 {
				assert.Empty(t, payload)
			} else {
				assert.Equal(t, tc.payload, payload)
			}
		})
	}
}

func TestReadFrameMultiple(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, proto.WriteFrame(&buf, proto.AttachFrameData, []byte("first")))
	require.NoError(t, proto.WriteFrame(&buf, proto.AttachFrameData, []byte("second")))

	_, p1, err := proto.ReadFrame(&buf)
	require.NoError(t, err)
	assert.Equal(t, []byte("first"), p1)

	_, p2, err := proto.ReadFrame(&buf)
	require.NoError(t, err)
	assert.Equal(t, []byte("second"), p2)
}
