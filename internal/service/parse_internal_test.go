package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSSE_SingleEvent(t *testing.T) {
	t.Parallel()
	raw := "event: hello\ndata: world\n\n"
	var got []Event
	parseSSE(strings.NewReader(raw), func(ev Event) bool {
		got = append(got, ev)
		return true
	})
	require.Len(t, got, 1)
	assert.Equal(t, "hello", got[0].Event)
	assert.Equal(t, "world", got[0].Data)
}

func TestParseSSE_MultipleEvents(t *testing.T) {
	t.Parallel()
	raw := "event: a\ndata: 1\n\nevent: b\ndata: 2\n\n"
	var got []Event
	parseSSE(strings.NewReader(raw), func(ev Event) bool {
		got = append(got, ev)
		return true
	})
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].Event)
	assert.Equal(t, "1", got[0].Data)
	assert.Equal(t, "b", got[1].Event)
	assert.Equal(t, "2", got[1].Data)
}

func TestParseSSE_EmitStopsParsing(t *testing.T) {
	t.Parallel()
	raw := "event: a\ndata: 1\n\nevent: b\ndata: 2\n\n"
	var got []Event
	parseSSE(strings.NewReader(raw), func(ev Event) bool {
		got = append(got, ev)
		return false // stop after first event
	})
	require.Len(t, got, 1)
	assert.Equal(t, "a", got[0].Event)
}

func TestParseSSE_IgnoresIncompleteTrailingEvent(t *testing.T) {
	t.Parallel()
	// No blank line after the final "data:" line — the event must
	// not be emitted because the delimiter hasn't been seen.
	raw := "event: a\ndata: 1\n\nevent: b\ndata: 2\n"
	var got []Event
	parseSSE(strings.NewReader(raw), func(ev Event) bool {
		got = append(got, ev)
		return true
	})
	require.Len(t, got, 1)
	assert.Equal(t, "a", got[0].Event)
}

func TestParseSSE_SkipsUnknownFields(t *testing.T) {
	t.Parallel()
	// Lines that are neither "event: " nor "data: " must be ignored
	// without breaking parsing.
	raw := "id: 42\nevent: hello\nretry: 1000\ndata: world\n\n"
	var got []Event
	parseSSE(strings.NewReader(raw), func(ev Event) bool {
		got = append(got, ev)
		return true
	})
	require.Len(t, got, 1)
	assert.Equal(t, "hello", got[0].Event)
	assert.Equal(t, "world", got[0].Data)
}
