package main

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v81/github"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// fakeKVStore backs a plugintest.API's KVSetWithOptions/KVDelete with real atomic
// compare-and-set semantics (mutex-guarded map), the same contract the real Mattermost
// server provides, so concurrency tests exercise genuine race behavior rather than a
// canned mock response.
type fakeKVStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (f *fakeKVStore) set(key string, value []byte, opts model.PluginKVSetOptions) (bool, *model.AppError) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.data == nil {
		f.data = make(map[string][]byte)
	}

	if opts.Atomic {
		existing, ok := f.data[key]
		if opts.OldValue == nil {
			if ok {
				return false, nil
			}
		} else if !ok || !bytes.Equal(existing, opts.OldValue) {
			return false, nil
		}
	}

	f.data[key] = value
	return true, nil
}

func (f *fakeKVStore) delete(key string) *model.AppError {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, key)
	return nil
}

// TestTryClaimPost_OnlyOneWinnerUnderConcurrency proves the atomic claim used to gate PR
// post creation genuinely allows only one winner when many goroutines race for the same tag.
func TestTryClaimPost_OnlyOneWinnerUnderConcurrency(t *testing.T) {
	store := &fakeKVStore{}
	mockAPI := &plugintest.API{}
	mockAPI.On("KVSetWithOptions", mock.Anything, mock.Anything, mock.Anything).Return(store.set)

	p := &Plugin{}
	p.API = mockAPI
	p.client = pluginapi.NewClient(mockAPI, nil)

	const concurrency = 20
	results := make([]bool, concurrency)

	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := range concurrency {
		go func(i int) {
			defer wg.Done()
			claimed, err := p.tryClaimPost("#owner.repo.1")
			require.NoError(t, err)
			results[i] = claimed
		}(i)
	}
	wg.Wait()

	winners := 0
	for _, claimed := range results {
		if claimed {
			winners++
		}
	}

	require.Equal(t, 1, winners, "expected exactly one goroutine to win the claim, got %d", winners)
}

// TestTryClaimPost_SetsExpiry ensures claims don't accumulate in the KV store forever: a
// successful claim must carry the postClaimTTL expiry, not persist indefinitely.
func TestTryClaimPost_SetsExpiry(t *testing.T) {
	mockAPI := &plugintest.API{}

	var gotOptions model.PluginKVSetOptions
	mockAPI.On("KVSetWithOptions", "post-claim:#owner.repo.1", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			gotOptions = args.Get(2).(model.PluginKVSetOptions)
		}).
		Return(true, nil)

	p := &Plugin{}
	p.API = mockAPI
	p.client = pluginapi.NewClient(mockAPI, nil)

	claimed, err := p.tryClaimPost("#owner.repo.1")
	require.NoError(t, err)
	require.True(t, claimed)

	require.True(t, gotOptions.Atomic)
	require.Nil(t, gotOptions.OldValue)
	require.Equal(t, int64(postClaimTTL/time.Second), gotOptions.ExpireInSeconds)
}

// TestHandlePullRequestUpdated_DoesNotDoublePost reproduces the reported bug: GitHub sends
// two webhook deliveries in quick succession for the same non-draft, reviewer-assigned pull
// request (e.g. "opened" and "review_requested"), and both are handled concurrently. Before
// the fix, both deliveries would observe no existing Mattermost post and both would call
// CreatePost, pinning the PR twice. After the fix, only one delivery should ever post.
func TestHandlePullRequestUpdated_DoesNotDoublePost(t *testing.T) {
	store := &fakeKVStore{}
	mockAPI := &plugintest.API{}

	botUserId := "bot-id"
	team := &model.Team{Id: "team-id", Name: "team"}
	channel := &model.Channel{Id: "channel-id", Name: "pr-feed"}

	mockAPI.On("GetTeamByName", "team").Return(team, nil)
	mockAPI.On("GetTeamMembers", "team-id", 0, 100).
		Return([]*model.TeamMember{{TeamId: "team-id", UserId: botUserId}}, nil)
	mockAPI.On("GetChannelByName", "team-id", "pr-feed", false).Return(channel, nil)
	mockAPI.On("SearchPostsInTeam", "team-id", mock.Anything).Return([]*model.Post{}, nil)
	mockAPI.On("KVSetWithOptions", mock.Anything, mock.Anything, mock.Anything).Return(store.set)
	mockAPI.On("CreatePost", mock.Anything).Return(&model.Post{Id: "post-id"}, nil)
	mockAPI.On("LogInfo", mock.Anything, mock.Anything, mock.Anything).Return()

	p := &Plugin{}
	p.API = mockAPI
	p.client = pluginapi.NewClient(mockAPI, nil)
	p.botUserId = &botUserId

	event := &github.PullRequestEvent{
		Repo: &github.Repository{
			Owner: &github.User{Login: github.Ptr("owner")},
			Name:  github.Ptr("repo"),
		},
		PullRequest: &github.PullRequest{
			Number:             github.Ptr(1),
			Title:              github.Ptr("Some PR"),
			HTMLURL:            github.Ptr("https://github.com/owner/repo/pull/1"),
			Draft:              github.Ptr(false),
			RequestedReviewers: []*github.User{{Login: github.Ptr("reviewer")}},
		},
	}

	// Simulate two concurrent webhook deliveries for the same pull request.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	for i := range 2 {
		go func(i int) {
			defer wg.Done()
			errs[i] = p.handlePullRequestUpdated("team", "pr-feed", event)
		}(i)
	}
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	mockAPI.AssertNumberOfCalls(t, "CreatePost", 1)
}
