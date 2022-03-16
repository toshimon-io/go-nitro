package engine

import (
	"bytes"
	"sort"
	"sync"

	"github.com/statechannels/go-nitro/channel"
	"github.com/statechannels/go-nitro/types"
)

// ChannelLocker is a utility class that allows for locking of channels to prevent concurrent updates to the same channels.
type ChannelLocker struct {
	channelLocks sync.Map
}

// NewChannelLocker returns a new ChannelLocker
func NewChannelLocker() *ChannelLocker {
	return &ChannelLocker{
		channelLocks: sync.Map{},
	}
}

// Lock acquires a locks on the given channels.
func (l *ChannelLocker) Lock(channelIds []types.Destination) {

	sorted := sortChannelIds(channelIds)

	for _, channelId := range sorted {
		result, _ := l.channelLocks.LoadOrStore(channelId, &sync.Mutex{})
		lock := result.(*sync.Mutex)
		lock.Lock()
	}
}

// Unlock releases the lock on the given channels.
func (l *ChannelLocker) Unlock(channelIds []types.Destination) {

	sorted := sortChannelIds(channelIds)

	for _, channelId := range sorted {
		result, _ := l.channelLocks.Load(channelId)
		lock := result.(*sync.Mutex)
		lock.Unlock()
	}
}

// SortChannelIds is a helper function to sort the channel ids.
// This is used to ensure that locks are acquired in the same order.
func sortChannelIds(channelIds []types.Destination) []types.Destination {
	sorted := make([]types.
		Destination, len(channelIds))
	copy(sorted, channelIds)
	sort.Slice(sorted, func(i, j int) bool { return bytes.Compare(channelIds[i].Bytes(), channelIds[j].Bytes()) < 0 })
	return sorted
}

// GetChannelIds is a helper function to get the channel ids from a collection of channels.
func GetChannelIds(channels []*channel.Channel) []types.Destination {
	channelIds := make([]types.Destination, len(channels))
	for i, channel := range channels {
		channelIds[i] = channel.Id
	}
	return channelIds
}
