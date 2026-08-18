package mongo

import "testing"

func TestAdvertisePortProxy(t *testing.T) {
	t.Setenv("SPROUT_PUBLIC_HOST", "strido.fit")
	t.Setenv("SPROUT_BRANCH_SUBDOMAIN", "true")
	t.Setenv("SPROUT_MONGO_PROXY", "")
	if !ProxyEnabled() || AdvertisePort(55461) != 27017 {
		t.Fatalf("proxy on: enabled=%v port=%d", ProxyEnabled(), AdvertisePort(55461))
	}
	t.Setenv("SPROUT_MONGO_PROXY", "false")
	if ProxyEnabled() || AdvertisePort(55461) != 55461 {
		t.Fatalf("proxy off: enabled=%v port=%d", ProxyEnabled(), AdvertisePort(55461))
	}
	t.Setenv("SPROUT_PUBLIC_HOST", "localhost")
	t.Setenv("SPROUT_MONGO_PROXY", "")
	t.Setenv("SPROUT_BRANCH_SUBDOMAIN", "")
	if ProxyEnabled() || AdvertisePort(55461) != 55461 {
		t.Fatalf("localhost: enabled=%v port=%d", ProxyEnabled(), AdvertisePort(55461))
	}
}
