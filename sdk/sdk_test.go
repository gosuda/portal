package sdk

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/core/cryptoops"
)

func newUnitTestClient(t *testing.T) *Client {
	t.Helper()

	client, err := NewClient(
		WithBootstrapServers(nil),
		WithHealthCheckInterval(time.Hour),
		WithReconnectInterval(time.Hour),
	)
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, client.Close())
	})

	return client
}

func newPipeRelayDialer(t *testing.T) func(context.Context, string) (portal.Session, error) {
	t.Helper()

	relayCred, err := cryptoops.NewCredential()
	require.NoError(t, err)

	relayServer := portal.NewRelayServer(relayCred, []string{"pipe://relay"})
	relayServer.Start()
	t.Cleanup(relayServer.Stop)

	return func(_ context.Context, _ string) (portal.Session, error) {
		clientSession, serverSession := portal.NewPipeSessionPair()
		go relayServer.HandleSession(serverSession)
		return clientSession, nil
	}
}

func TestClientRejectsMalformedBootstrapServer(t *testing.T) {
	var dialCalls atomic.Int32

	client, err := NewClient(
		WithBootstrapServers([]string{""}),
		WithDialer(func(_ context.Context, _ string) (portal.Session, error) {
			dialCalls.Add(1)
			return nil, errors.New("dial should not be called for malformed bootstrap")
		}),
	)

	require.Nil(t, client)
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to connect to any bootstrap servers")
	require.Zero(t, dialCalls.Load())
}

func TestAddRelayRejectsDuplicateRelay(t *testing.T) {
	client := newUnitTestClient(t)
	dialer := newPipeRelayDialer(t)

	var dialCalls atomic.Int32
	countingDialer := func(ctx context.Context, addr string) (portal.Session, error) {
		dialCalls.Add(1)
		return dialer(ctx, addr)
	}

	const relayAddr = "pipe://relay-duplicate"
	require.NoError(t, client.AddRelay(relayAddr, countingDialer))
	require.ElementsMatch(t, []string{relayAddr}, client.GetRelays())

	err := client.AddRelay(relayAddr, countingDialer)
	require.ErrorIs(t, err, ErrRelayExists)
	require.EqualValues(t, 1, dialCalls.Load())
	require.ElementsMatch(t, []string{relayAddr}, client.GetRelays())
}

func TestAddRelayReturnsDialerErrorWithoutStateMutation(t *testing.T) {
	client := newUnitTestClient(t)

	expectedErr := errors.New("dial failed")
	const relayAddr = "pipe://relay-unreachable"

	err := client.AddRelay(relayAddr, func(_ context.Context, gotAddr string) (portal.Session, error) {
		require.Equal(t, relayAddr, gotAddr)
		return nil, expectedErr
	})
	require.ErrorIs(t, err, expectedErr)
	require.Empty(t, client.GetRelays())
}

func TestRemoveRelayStateTransitionsAndMissingRemoval(t *testing.T) {
	client := newUnitTestClient(t)
	dialer := newPipeRelayDialer(t)

	const relayAddr = "pipe://relay-remove"
	require.NoError(t, client.AddRelay(relayAddr, dialer))
	require.ElementsMatch(t, []string{relayAddr}, client.GetRelays())

	require.NoError(t, client.RemoveRelay(relayAddr))
	require.Empty(t, client.GetRelays())

	err := client.RemoveRelay(relayAddr)
	require.ErrorIs(t, err, ErrRelayNotFound)
}

func TestGetRelaysReturnsSnapshot(t *testing.T) {
	client := newUnitTestClient(t)
	dialer := newPipeRelayDialer(t)

	const relayA = "pipe://relay-a"
	const relayB = "pipe://relay-b"
	require.NoError(t, client.AddRelay(relayA, dialer))
	require.NoError(t, client.AddRelay(relayB, dialer))

	initial := client.GetRelays()
	require.ElementsMatch(t, []string{relayA, relayB}, initial)

	initial[0] = "pipe://mutated"

	current := client.GetRelays()
	require.ElementsMatch(t, []string{relayA, relayB}, current)
	require.NotContains(t, current, "pipe://mutated")
}

func TestLookupNameReturnsErrNoAvailableRelayWhenNoRelays(t *testing.T) {
	client := newUnitTestClient(t)

	lease, err := client.LookupName("missing")
	require.Nil(t, lease)
	require.ErrorIs(t, err, ErrNoAvailableRelay)
}
