package relaydns

import (
	"fmt"
	"testing"
	"time"

	"github.com/cockroachdb/crlib/testutils/require"
)

func TestFetchMultiAddrsFromHosts(t *testing.T) {
	url := "http://relaydns.gosuda.org"
	timeout := 5 * time.Second

	addrs, err := FetchMultiaddrsFromHosts(url, timeout)
	require.NoError(t, err)
	fmt.Println(addrs)
}
