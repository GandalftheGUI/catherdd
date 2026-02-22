package daemon

import (
	"testing"
	"time"

	"github.com/ianremillard/grove/internal/proto"
	"github.com/stretchr/testify/assert"
)

func TestEnvWith(t *testing.T) {
	base := []string{"A=1", "B=2", "C=3"}

	t.Run("overrides existing key", func(t *testing.T) {
		result := envWith(base, "A=99")
		assert.Contains(t, result, "A=99")
		assert.NotContains(t, result, "A=1")
		assert.Contains(t, result, "B=2")
		assert.Contains(t, result, "C=3")
	})

	t.Run("adds new key", func(t *testing.T) {
		result := envWith(base, "D=4")
		assert.Contains(t, result, "D=4")
		assert.Contains(t, result, "A=1")
		assert.Contains(t, result, "B=2")
	})

	t.Run("multiple overrides", func(t *testing.T) {
		result := envWith(base, "A=99", "B=88")
		assert.Contains(t, result, "A=99")
		assert.Contains(t, result, "B=88")
		assert.NotContains(t, result, "A=1")
		assert.NotContains(t, result, "B=2")
		assert.Contains(t, result, "C=3")
	})

	t.Run("empty base", func(t *testing.T) {
		result := envWith(nil, "X=1")
		assert.Equal(t, []string{"X=1"}, result)
	})

	t.Run("no overrides", func(t *testing.T) {
		result := envWith(base)
		assert.ElementsMatch(t, base, result)
	})
}

func TestInfoWaitingPromotion(t *testing.T) {
	inst := &Instance{
		ID:             "1",
		Project:        "my-app",
		Branch:         "main",
		CreatedAt:      time.Now().Add(-10 * time.Minute),
		state:          proto.StateRunning,
		lastOutputTime: time.Now().Add(-3 * time.Second), // idle >2s
	}

	info := inst.Info()
	assert.Equal(t, proto.StateWaiting, info.State)
}

func TestInfoRunningWhenRecentOutput(t *testing.T) {
	inst := &Instance{
		ID:             "1",
		Project:        "my-app",
		Branch:         "main",
		CreatedAt:      time.Now().Add(-1 * time.Minute),
		state:          proto.StateRunning,
		lastOutputTime: time.Now(), // just produced output
	}

	info := inst.Info()
	assert.Equal(t, proto.StateRunning, info.State)
}

func TestInfoNonRunningStateUnchanged(t *testing.T) {
	for _, state := range []string{
		proto.StateExited, proto.StateCrashed, proto.StateKilled, proto.StateFinished,
	} {
		inst := &Instance{
			ID:             "1",
			state:          state,
			lastOutputTime: time.Now().Add(-10 * time.Second),
		}
		assert.Equal(t, state, inst.Info().State, "state %s should not be promoted", state)
	}
}
