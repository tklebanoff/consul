package xds

import (
	"bytes"
	"path/filepath"
	"sort"
	"testing"
	"text/template"
	"time"

	envoy_listener_v3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	testinf "github.com/mitchellh/go-testing-interface"
	"github.com/stretchr/testify/require"

	"github.com/hashicorp/consul/agent/connect"
	"github.com/hashicorp/consul/agent/consul/discoverychain"
	"github.com/hashicorp/consul/agent/proxycfg"
	"github.com/hashicorp/consul/agent/structs"
	"github.com/hashicorp/consul/agent/xds/proxysupport"
	"github.com/hashicorp/consul/sdk/testutil"
	"github.com/hashicorp/consul/types"
)

func TestListenersFromSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	tests := []struct {
		name   string
		create func(t testinf.T) *proxycfg.ConfigSnapshot
		// Setup is called before the test starts. It is passed the snapshot from
		// TestConfigSnapshot and is allowed to modify it in any way to setup the
		// test input.
		setup              func(snap *proxycfg.ConfigSnapshot)
		overrideGoldenName string
		serverSetup        func(*Server)
	}{
		{
			name:   "defaults",
			create: proxycfg.TestConfigSnapshot,
			setup:  nil, // Default snapshot
		},
		{
			name:   "listener-bind-address",
			create: proxycfg.TestConfigSnapshot,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Config["bind_address"] = "127.0.0.2"
			},
		},
		{
			name:   "listener-bind-port",
			create: proxycfg.TestConfigSnapshot,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Config["bind_port"] = 8888
			},
		},
		{
			name:   "listener-bind-address-port",
			create: proxycfg.TestConfigSnapshot,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Config["bind_address"] = "127.0.0.2"
				snap.Proxy.Config["bind_port"] = 8888
			},
		},
		{
			name:   "http-public-listener",
			create: proxycfg.TestConfigSnapshot,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Config["protocol"] = "http"
			},
		},
		{
			name:   "http-listener-with-timeouts",
			create: proxycfg.TestConfigSnapshot,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Config["protocol"] = "http"
				snap.Proxy.Config["local_connect_timeout_ms"] = 1234
				snap.Proxy.Config["local_request_timeout_ms"] = 2345
			},
		},
		{
			name:   "http-upstream",
			create: proxycfg.TestConfigSnapshot,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Upstreams[0].Config["protocol"] = "http"
			},
		},
		{
			name:   "custom-public-listener",
			create: proxycfg.TestConfigSnapshot,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Config["envoy_public_listener_json"] =
					customListenerJSON(t, customListenerJSONOptions{
						Name: "custom-public-listen",
					})
			},
		},
		{
			name:   "custom-public-listener-http",
			create: proxycfg.TestConfigSnapshot,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Config["protocol"] = "http"
				snap.Proxy.Config["envoy_public_listener_json"] =
					customHTTPListenerJSON(t, customHTTPListenerJSONOptions{
						Name: "custom-public-listen",
					})
			},
		},
		{
			name:   "custom-public-listener-http-2",
			create: proxycfg.TestConfigSnapshot,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Config["protocol"] = "http"
				snap.Proxy.Config["envoy_public_listener_json"] =
					customHTTPListenerJSON(t, customHTTPListenerJSONOptions{
						Name:                      "custom-public-listen",
						HTTPConnectionManagerName: httpConnectionManagerNewName,
					})
			},
		},
		{
			name:   "custom-public-listener-http-missing",
			create: proxycfg.TestConfigSnapshot,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Config["protocol"] = "http"
				snap.Proxy.Config["envoy_public_listener_json"] =
					customListenerJSON(t, customListenerJSONOptions{
						Name: "custom-public-listen",
					})
			},
		},
		{
			name:               "custom-public-listener-ignores-tls",
			create:             proxycfg.TestConfigSnapshot,
			overrideGoldenName: "custom-public-listener", // should be the same
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Config["envoy_public_listener_json"] =
					customListenerJSON(t, customListenerJSONOptions{
						Name: "custom-public-listen",
						// Attempt to override the TLS context should be ignored
						TLSContext: `"allowRenegotiation": false`,
					})
			},
		},
		{
			name:   "custom-upstream",
			create: proxycfg.TestConfigSnapshot,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Upstreams[0].Config["envoy_listener_json"] =
					customListenerJSON(t, customListenerJSONOptions{
						Name: "custom-upstream",
					})
			},
		},
		{
			name:   "custom-upstream-ignored-with-disco-chain",
			create: proxycfg.TestConfigSnapshotDiscoveryChainWithFailover,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Upstreams[0].Config["envoy_listener_json"] =
					customListenerJSON(t, customListenerJSONOptions{
						Name: "custom-upstream",
					})
			},
		},
		{
			name:   "splitter-with-resolver-redirect",
			create: proxycfg.TestConfigSnapshotDiscoveryChain_SplitterWithResolverRedirectMultiDC,
			setup:  nil,
		},
		{
			name:   "connect-proxy-with-tcp-chain",
			create: proxycfg.TestConfigSnapshotDiscoveryChain,
			setup:  nil,
		},
		{
			name: "connect-proxy-with-http-chain",
			create: func(t testinf.T) *proxycfg.ConfigSnapshot {
				return proxycfg.TestConfigSnapshotDiscoveryChainWithEntries(t,
					&structs.ProxyConfigEntry{
						Kind: structs.ProxyDefaults,
						Name: structs.ProxyConfigGlobal,
						Config: map[string]interface{}{
							"protocol": "http",
						},
					},
				)
			},
			setup: nil,
		},
		{
			name: "connect-proxy-with-http2-chain",
			create: func(t testinf.T) *proxycfg.ConfigSnapshot {
				return proxycfg.TestConfigSnapshotDiscoveryChainWithEntries(t,
					&structs.ProxyConfigEntry{
						Kind: structs.ProxyDefaults,
						Name: structs.ProxyConfigGlobal,
						Config: map[string]interface{}{
							"protocol": "http2",
						},
					},
				)
			},
			setup: nil,
		},
		{
			name: "connect-proxy-with-grpc-chain",
			create: func(t testinf.T) *proxycfg.ConfigSnapshot {
				return proxycfg.TestConfigSnapshotDiscoveryChainWithEntries(t,
					&structs.ProxyConfigEntry{
						Kind: structs.ProxyDefaults,
						Name: structs.ProxyConfigGlobal,
						Config: map[string]interface{}{
							"protocol": "grpc",
						},
					},
				)
			},
			setup: nil,
		},
		{
			name:   "connect-proxy-with-chain-external-sni",
			create: proxycfg.TestConfigSnapshotDiscoveryChainExternalSNI,
			setup:  nil,
		},
		{
			name:   "connect-proxy-with-chain-and-overrides",
			create: proxycfg.TestConfigSnapshotDiscoveryChainWithOverrides,
			setup:  nil,
		},
		{
			name:   "connect-proxy-with-tcp-chain-failover-through-remote-gateway",
			create: proxycfg.TestConfigSnapshotDiscoveryChainWithFailoverThroughRemoteGateway,
			setup:  nil,
		},
		{
			name:   "connect-proxy-with-tcp-chain-failover-through-local-gateway",
			create: proxycfg.TestConfigSnapshotDiscoveryChainWithFailoverThroughLocalGateway,
			setup:  nil,
		},
		{
			name:   "expose-paths-local-app-paths",
			create: proxycfg.TestConfigSnapshotExposeConfig,
		},
		{
			name:   "expose-paths-new-cluster-http2",
			create: proxycfg.TestConfigSnapshotExposeConfig,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Expose.Paths[1] = structs.ExposePath{
					LocalPathPort: 9090,
					Path:          "/grpc.health.v1.Health/Check",
					ListenerPort:  21501,
					Protocol:      "http2",
				}
			},
		},
		{
			// NOTE: if IPv6 is not supported in the kernel per
			// kernelSupportsIPv6() then this test will fail because the golden
			// files were generated assuming ipv6 support was present
			name:   "expose-checks",
			create: proxycfg.TestConfigSnapshotExposeConfig,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Expose = structs.ExposeConfig{
					Checks: true,
				}
			},
			serverSetup: func(s *Server) {
				s.CfgFetcher = configFetcherFunc(func() string {
					return "192.0.2.1"
				})

				s.CheckFetcher = httpCheckFetcherFunc(func(sid structs.ServiceID) []structs.CheckType {
					if sid != structs.NewServiceID("web", nil) {
						return nil
					}
					return []structs.CheckType{{
						CheckID:   types.CheckID("http"),
						Name:      "http",
						HTTP:      "http://127.0.0.1:8181/debug",
						ProxyHTTP: "http://:21500/debug",
						Method:    "GET",
						Interval:  10 * time.Second,
						Timeout:   1 * time.Second,
					}}
				})
			},
		},
		{
			name:   "mesh-gateway",
			create: proxycfg.TestConfigSnapshotMeshGateway,
		},
		{
			name:   "mesh-gateway-using-federation-states",
			create: proxycfg.TestConfigSnapshotMeshGatewayUsingFederationStates,
		},
		{
			name:   "mesh-gateway-no-services",
			create: proxycfg.TestConfigSnapshotMeshGatewayNoServices,
		},
		{
			name:   "mesh-gateway-tagged-addresses",
			create: proxycfg.TestConfigSnapshotMeshGateway,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Config = map[string]interface{}{
					"envoy_mesh_gateway_no_default_bind":       true,
					"envoy_mesh_gateway_bind_tagged_addresses": true,
				}
			},
		},
		{
			name:   "mesh-gateway-custom-addresses",
			create: proxycfg.TestConfigSnapshotMeshGateway,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Config = map[string]interface{}{
					"envoy_mesh_gateway_bind_addresses": map[string]structs.ServiceAddress{
						"foo": {
							Address: "198.17.2.3",
							Port:    8080,
						},
						"bar": {
							Address: "2001:db8::ff",
							Port:    9999,
						},
						"baz": {
							Address: "127.0.0.1",
							Port:    8765,
						},
					},
				}
			},
		},
		{
			name:   "ingress-gateway",
			create: proxycfg.TestConfigSnapshotIngressGateway,
			setup:  nil,
		},
		{
			name:   "ingress-gateway-bind-addrs",
			create: proxycfg.TestConfigSnapshotIngressGateway,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.TaggedAddresses = map[string]structs.ServiceAddress{
					"lan": {Address: "10.0.0.1"},
					"wan": {Address: "172.16.0.1"},
				}
				snap.Proxy.Config = map[string]interface{}{
					"envoy_gateway_no_default_bind":       true,
					"envoy_gateway_bind_tagged_addresses": true,
					"envoy_gateway_bind_addresses": map[string]structs.ServiceAddress{
						"foo": {Address: "8.8.8.8"},
					},
				}
			},
		},
		{
			name:   "ingress-gateway-no-services",
			create: proxycfg.TestConfigSnapshotIngressGatewayNoServices,
			setup:  nil,
		},
		{
			name:   "ingress-with-chain-external-sni",
			create: proxycfg.TestConfigSnapshotIngressExternalSNI,
			setup:  nil,
		},
		{
			name:   "ingress-with-chain-and-overrides",
			create: proxycfg.TestConfigSnapshotIngressWithOverrides,
			setup:  nil,
		},
		{
			name:   "ingress-with-tcp-chain-failover-through-remote-gateway",
			create: proxycfg.TestConfigSnapshotIngressWithFailoverThroughRemoteGateway,
			setup:  nil,
		},
		{
			name:   "ingress-with-tcp-chain-failover-through-local-gateway",
			create: proxycfg.TestConfigSnapshotIngressWithFailoverThroughLocalGateway,
			setup:  nil,
		},
		{
			name:   "ingress-splitter-with-resolver-redirect",
			create: proxycfg.TestConfigSnapshotIngress_SplitterWithResolverRedirectMultiDC,
			setup:  nil,
		},
		{
			name:   "terminating-gateway",
			create: proxycfg.TestConfigSnapshotTerminatingGateway,
			setup:  nil,
		},
		{
			name:   "terminating-gateway-no-services",
			create: proxycfg.TestConfigSnapshotTerminatingGatewayNoServices,
			setup:  nil,
		},
		{
			name:   "terminating-gateway-custom-and-tagged-addresses",
			create: proxycfg.TestConfigSnapshotTerminatingGateway,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Config = map[string]interface{}{
					"envoy_gateway_no_default_bind":       true,
					"envoy_gateway_bind_tagged_addresses": true,
					"envoy_gateway_bind_addresses": map[string]structs.ServiceAddress{
						// This bind address should not get a listener due to deduplication and it sorts to the end
						"z-duplicate-of-tagged-wan-addr": {
							Address: "198.18.0.1",
							Port:    443,
						},
						"foo": {
							Address: "198.17.2.3",
							Port:    8080,
						},
					},
				}
			},
		},
		{
			name:   "terminating-gateway-service-subsets",
			create: proxycfg.TestConfigSnapshotTerminatingGateway,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.TerminatingGateway.ServiceResolvers = map[structs.ServiceName]*structs.ServiceResolverConfigEntry{
					structs.NewServiceName("web", nil): {
						Kind: structs.ServiceResolver,
						Name: "web",
						Subsets: map[string]structs.ServiceResolverSubset{
							"v1": {
								Filter: "Service.Meta.version == 1",
							},
							"v2": {
								Filter:      "Service.Meta.version == 2",
								OnlyPassing: true,
							},
						},
					},
				}
				snap.TerminatingGateway.ServiceConfigs[structs.NewServiceName("web", nil)] = &structs.ServiceConfigResponse{
					ProxyConfig: map[string]interface{}{"protocol": "http"},
				}
			},
		},
		{
			name:   "ingress-http-multiple-services",
			create: proxycfg.TestConfigSnapshotIngress_HTTPMultipleServices,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.IngressGateway.Upstreams = map[proxycfg.IngressListenerKey]structs.Upstreams{
					{Protocol: "http", Port: 8080}: {
						{
							DestinationName: "foo",
							LocalBindPort:   8080,
						},
						{
							DestinationName: "bar",
							LocalBindPort:   8080,
						},
					},
					{Protocol: "http", Port: 443}: {
						{
							DestinationName: "baz",
							LocalBindPort:   443,
						},
						{
							DestinationName: "qux",
							LocalBindPort:   443,
						},
					},
				}
			},
		},
		{
			name:   "terminating-gateway-no-api-cert",
			create: proxycfg.TestConfigSnapshotTerminatingGateway,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.TerminatingGateway.ServiceLeaves[structs.NewServiceName("api", nil)] = nil
			},
		},
		{
			name:   "ingress-with-tls-listener",
			create: proxycfg.TestConfigSnapshotIngressWithTLSListener,
			setup:  nil,
		},
		{
			name:   "transparent-proxy",
			create: proxycfg.TestConfigSnapshot,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Mode = structs.ProxyModeTransparent

				snap.ConnectProxy.ClusterConfigSet = true

				// DiscoveryChain without an UpstreamConfig should yield a filter chain when in transparent proxy mode
				snap.ConnectProxy.DiscoveryChain["google"] = discoverychain.TestCompileConfigEntries(
					t, "google", "default", "dc1",
					connect.TestClusterID+".consul", "dc1", nil)
				snap.ConnectProxy.WatchedUpstreamEndpoints["google"] = map[string]structs.CheckServiceNodes{
					"google.default.dc1": {
						structs.CheckServiceNode{
							Node: &structs.Node{
								Address:    "8.8.8.8",
								Datacenter: "dc1",
							},
							Service: &structs.NodeService{
								Service: "google",
								Port:    9090,
							},
						},
					},
				}

				// DiscoveryChains without endpoints do not get a filter chain because there are no addresses to match on.
				snap.ConnectProxy.DiscoveryChain["no-endpoints"] = discoverychain.TestCompileConfigEntries(
					t, "no-endpoints", "default", "dc1",
					connect.TestClusterID+".consul", "dc1", nil)
			},
		},
		{
			name:   "transparent-proxy-catalog-destinations-only",
			create: proxycfg.TestConfigSnapshot,
			setup: func(snap *proxycfg.ConfigSnapshot) {
				snap.Proxy.Mode = structs.ProxyModeTransparent

				snap.ConnectProxy.ClusterConfigSet = true
				snap.ConnectProxy.ClusterConfig = &structs.ClusterConfigEntry{
					Kind: structs.ClusterConfig,
					Name: structs.ClusterConfigCluster,
					TransparentProxy: structs.TransparentProxyClusterConfig{
						CatalogDestinationsOnly: true,
					},
				}

				// DiscoveryChain without an UpstreamConfig should yield a filter chain when in transparent proxy mode
				snap.ConnectProxy.DiscoveryChain["google"] = discoverychain.TestCompileConfigEntries(
					t, "google", "default", "dc1",
					connect.TestClusterID+".consul", "dc1", nil)
				snap.ConnectProxy.WatchedUpstreamEndpoints["google"] = map[string]structs.CheckServiceNodes{
					"google.default.dc1": {
						structs.CheckServiceNode{
							Node: &structs.Node{
								Address:    "8.8.8.8",
								Datacenter: "dc1",
							},
							Service: &structs.NodeService{
								Service: "google",
								Port:    9090,
							},
						},
					},
				}

				// DiscoveryChains without endpoints do not get a filter chain because there are no addresses to match on.
				snap.ConnectProxy.DiscoveryChain["no-endpoints"] = discoverychain.TestCompileConfigEntries(
					t, "no-endpoints", "default", "dc1",
					connect.TestClusterID+".consul", "dc1", nil)
			},
		},
	}

	latestEnvoyVersion := proxysupport.EnvoyVersions[0]
	for _, envoyVersion := range proxysupport.EnvoyVersions {
		sf, err := determineSupportedProxyFeaturesFromString(envoyVersion)
		require.NoError(t, err)
		t.Run("envoy-"+envoyVersion, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					// Sanity check default with no overrides first
					snap := tt.create(t)

					// We need to replace the TLS certs with deterministic ones to make golden
					// files workable. Note we don't update these otherwise they'd change
					// golder files for every test case and so not be any use!
					setupTLSRootsAndLeaf(t, snap)

					if tt.setup != nil {
						tt.setup(snap)
					}

					// Need server just for logger dependency
					s := Server{Logger: testutil.Logger(t)}
					if tt.serverSetup != nil {
						tt.serverSetup(&s)
					}

					cInfo := connectionInfo{
						Token:         "my-token",
						ProxyFeatures: sf,
					}
					listeners, err := s.listenersFromSnapshot(cInfo, snap)
					require.NoError(t, err)

					// The order of listeners returned via LDS isn't relevant, so it's safe
					// to sort these for the purposes of test comparisons.
					sort.Slice(listeners, func(i, j int) bool {
						return listeners[i].(*envoy_listener_v3.Listener).Name < listeners[j].(*envoy_listener_v3.Listener).Name
					})

					r, err := createResponse(ListenerType, "00000001", "00000001", listeners)
					require.NoError(t, err)

					t.Run("current", func(t *testing.T) {
						gotJSON := protoToJSON(t, r)

						gName := tt.name
						if tt.overrideGoldenName != "" {
							gName = tt.overrideGoldenName
						}

						require.JSONEq(t, goldenEnvoy(t, filepath.Join("listeners", gName), envoyVersion, latestEnvoyVersion, gotJSON), gotJSON)
					})

					t.Run("v2-compat", func(t *testing.T) {
						respV2, err := convertDiscoveryResponseToV2(r)
						require.NoError(t, err)

						gotJSON := protoToJSON(t, respV2)

						gName := tt.name
						if tt.overrideGoldenName != "" {
							gName = tt.overrideGoldenName
						}

						gName += ".v2compat"

						require.JSONEq(t, goldenEnvoy(t, filepath.Join("listeners", gName), envoyVersion, latestEnvoyVersion, gotJSON), gotJSON)
					})
				})
			}
		})
	}
}

func expectListenerJSONResources(snap *proxycfg.ConfigSnapshot) map[string]string {
	return map[string]string{
		"public_listener": `{
				"@type": "type.googleapis.com/envoy.config.listener.v3.Listener",
				"name": "public_listener:0.0.0.0:9999",
				"address": {
					"socketAddress": {
						"address": "0.0.0.0",
						"portValue": 9999
					}
				},
				"trafficDirection": "INBOUND",
				"filterChains": [
					{
						"transportSocket": ` + expectedPublicTransportSocketJSON(snap) + `,
						"filters": [
							{
								"name": "envoy.filters.network.rbac",
								"typedConfig": {
									"@type": "type.googleapis.com/envoy.extensions.filters.network.rbac.v3.RBAC",
										"rules": {
											},
										"statPrefix": "connect_authz"
									}
							},
							{
								"name": "envoy.filters.network.tcp_proxy",
								"typedConfig": {
									"@type": "type.googleapis.com/envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy",
									"cluster": "local_app",
									"statPrefix": "public_listener"
								}
							}
						]
					}
				]
			}`,
		"db": `{
			"@type": "type.googleapis.com/envoy.config.listener.v3.Listener",
			"name": "db:127.0.0.1:9191",
			"address": {
				"socketAddress": {
					"address": "127.0.0.1",
					"portValue": 9191
				}
			},
			"trafficDirection": "OUTBOUND",
			"filterChains": [
				{
					"filters": [
						{
							"name": "envoy.filters.network.tcp_proxy",
							"typedConfig": {
								"@type": "type.googleapis.com/envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy",
								"cluster": "db.default.dc1.internal.11111111-2222-3333-4444-555555555555.consul",
								"statPrefix": "upstream.db.default.dc1"
							}
						}
					]
				}
			]
		}`,
		"prepared_query:geo-cache": `{
			"@type": "type.googleapis.com/envoy.config.listener.v3.Listener",
			"name": "prepared_query:geo-cache:127.10.10.10:8181",
			"address": {
				"socketAddress": {
					"address": "127.10.10.10",
					"portValue": 8181
				}
			},
			"trafficDirection": "OUTBOUND",
			"filterChains": [
				{
					"filters": [
						{
							"name": "envoy.filters.network.tcp_proxy",
							"typedConfig": {
								"@type": "type.googleapis.com/envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy",
								"cluster": "geo-cache.default.dc1.query.11111111-2222-3333-4444-555555555555.consul",
								"statPrefix": "upstream.prepared_query_geo-cache"
							}
						}
					]
				}
			]
		}`,
	}
}

func expectListenerJSONFromResources(snap *proxycfg.ConfigSnapshot, v, n uint64, resourcesJSON map[string]string) string {
	resJSON := ""
	// Sort resources into specific order because that matters in JSONEq
	// comparison later.
	keyOrder := []string{"public_listener"}
	for _, u := range snap.Proxy.Upstreams {
		keyOrder = append(keyOrder, u.Identifier())
	}
	for _, k := range keyOrder {
		j, ok := resourcesJSON[k]
		if !ok {
			continue
		}
		if resJSON != "" {
			resJSON += ",\n"
		}
		resJSON += j
	}
	return `{
		"versionInfo": "` + hexString(v) + `",
		"resources": [` + resJSON + `],
		"typeUrl": "type.googleapis.com/envoy.config.listener.v3.Listener",
		"nonce": "` + hexString(n) + `"
		}`
}

func expectListenerJSON(snap *proxycfg.ConfigSnapshot, v, n uint64) string {
	return expectListenerJSONFromResources(snap, v, n, expectListenerJSONResources(snap))
}

type customListenerJSONOptions struct {
	Name       string
	TLSContext string
}

const customListenerJSONTpl = `{
	"@type": "type.googleapis.com/envoy.config.listener.v3.Listener",
	"name": "{{ .Name }}",
	"address": {
		"socketAddress": {
			"address": "11.11.11.11",
			"portValue": 11111
		}
	},
	"filterChains": [
		{
			{{ if .TLSContext -}}
			"transport_socket": {
				"name": "tls",
				"typed_config": {
					"@type": "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext",
					{{ .TLSContext }}
				}
			},
			{{- end }}
			"filters": [
				{
					"name": "envoy.filters.network.tcp_proxy",
					"typedConfig": {
						"@type": "type.googleapis.com/envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy",
							"cluster": "random-cluster",
							"statPrefix": "foo-stats"
						}
				}
			]
		}
	]
}`

type customHTTPListenerJSONOptions struct {
	Name                      string
	HTTPConnectionManagerName string
}

const customHTTPListenerJSONTpl = `{
	"@type": "type.googleapis.com/envoy.config.listener.v3.Listener",
	"name": "{{ .Name }}",
	"address": {
		"socketAddress": {
			"address": "11.11.11.11",
			"portValue": 11111
		}
	},
	"filterChains": [
		{
			"filters": [
				{
					"name": "{{ .HTTPConnectionManagerName }}",
					"typedConfig": {
						"@type": "type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager",
						"http_filters": [
							{
								"name": "envoy.filters.http.router"
							}
						],
						"route_config": {
							"name": "public_listener",
							"virtual_hosts": [
								{
									"domains": [
										"*"
									],
									"name": "public_listener",
									"routes": [
										{
											"match": {
												"prefix": "/"
											},
											"route": {
												"cluster": "random-cluster"
											}
										}
									]
								}
							]
						}
					}
				}
			]
		}
	]
}`

var (
	customListenerJSONTemplate     = template.Must(template.New("").Parse(customListenerJSONTpl))
	customHTTPListenerJSONTemplate = template.Must(template.New("").Parse(customHTTPListenerJSONTpl))
)

func customListenerJSON(t *testing.T, opts customListenerJSONOptions) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, customListenerJSONTemplate.Execute(&buf, opts))
	return buf.String()
}

func customHTTPListenerJSON(t *testing.T, opts customHTTPListenerJSONOptions) string {
	t.Helper()
	if opts.HTTPConnectionManagerName == "" {
		opts.HTTPConnectionManagerName = httpConnectionManagerNewName
	}
	var buf bytes.Buffer
	require.NoError(t, customHTTPListenerJSONTemplate.Execute(&buf, opts))
	return buf.String()
}

type httpCheckFetcherFunc func(serviceID structs.ServiceID) []structs.CheckType

var _ HTTPCheckFetcher = (httpCheckFetcherFunc)(nil)

func (f httpCheckFetcherFunc) ServiceHTTPBasedChecks(serviceID structs.ServiceID) []structs.CheckType {
	return f(serviceID)
}

type configFetcherFunc func() string

var _ ConfigFetcher = (configFetcherFunc)(nil)

func (f configFetcherFunc) AdvertiseAddrLAN() string {
	return f()
}
