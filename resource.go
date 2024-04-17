// Copyright 2020 Envoyproxy Authors
//
//   Licensed under the Apache License, Version 2.0 (the "License");
//   you may not use this file except in compliance with the License.
//   You may obtain a copy of the License at
//
//       http://www.apache.org/licenses/LICENSE-2.0
//
//   Unless required by applicable law or agreed to in writing, software
//   distributed under the License is distributed on an "AS IS" BASIS,
//   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//   See the License for the specific language governing permissions and
//   limitations under the License.

package main

import (
	"os"
	"time"

	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"

	cluster "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpoint "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listener "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	route "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	router "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	original_source "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/listener/original_src/v3"
	tls_inspector "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/listener/tls_inspector/v3"
	hcm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	tcp_proxy "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/tcp_proxy/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/envoyproxy/go-control-plane/pkg/resource/v3"
)

func makeCluster(route Route) *cluster.Cluster {
	var endpoints []*endpoint.LbEndpoint

	for _, targetSv := range route.target_servers {
		port, _ := targetSv.Port.Int64()

		endpoints = append(endpoints, &endpoint.LbEndpoint{
			HostIdentifier: &endpoint.LbEndpoint_Endpoint{
				Endpoint: &endpoint.Endpoint{
					Address: &core.Address{
						Address: &core.Address_SocketAddress{
							SocketAddress: &core.SocketAddress{
								Protocol: core.SocketAddress_TCP,
								Address:  targetSv.Ip,
								PortSpecifier: &core.SocketAddress_PortValue{
									PortValue: uint32(port),
								},
							},
						},
					},
				},
			},
		})
	}

	return &cluster.Cluster{
		Name:                 route.uuid,
		ConnectTimeout:       durationpb.New(5 * time.Second),
		ClusterDiscoveryType: &cluster.Cluster_Type{Type: cluster.Cluster_LOGICAL_DNS},
		LbPolicy:             cluster.Cluster_ROUND_ROBIN,
		LoadAssignment: &endpoint.ClusterLoadAssignment{
			ClusterName: route.uuid,
			Endpoints: []*endpoint.LocalityLbEndpoints{{
				LbEndpoints: endpoints,
			}},
		},
		DnsLookupFamily: cluster.Cluster_V4_ONLY,
	}
}

func makeClusters(routes []Route) []types.Resource {
	var envoyListeners []types.Resource

	for _, route := range routes {
		envoyListeners = append(envoyListeners, makeCluster(route))
	}

	return envoyListeners
}

func makeRoute(mlistener Listener, routes []Route) *route.RouteConfiguration {
	var virtualHosts []*route.VirtualHost

	for _, routec := range routes {
		if routec.listener != mlistener.uuid {
			continue
		}
		virtualHosts = append(virtualHosts, &route.VirtualHost{
			Name:    routec.uuid,
			Domains: routec.domain_names,
			Routes: []*route.Route{{
				Match: &route.RouteMatch{
					PathSpecifier: &route.RouteMatch_Prefix{
						Prefix: "/",
					},
				},
				Action: &route.Route_Route{
					Route: &route.RouteAction{
						ClusterSpecifier: &route.RouteAction_Cluster{
							Cluster: routec.uuid,
						},
					},
				},
			}},
		})
	}

	return &route.RouteConfiguration{
		Name:         mlistener.uuid + "-route",
		VirtualHosts: virtualHosts,
	}
}

func makeRoutes(listeners []Listener, routes []Route) []types.Resource {
	var envoyListeners []types.Resource

	for _, listener := range listeners {
		if listener.stype != "http" {
			continue
		}
		envoyListeners = append(envoyListeners, makeRoute(listener, routes))
	}

	return envoyListeners
}

func createHttpListener(mlistener Listener, routes []Route) *listener.Listener {
	routerConfig, _ := anypb.New(&router.Router{})
	// HTTP filter configuration
	manager := &hcm.HttpConnectionManager{
		CodecType:  hcm.HttpConnectionManager_AUTO,
		StatPrefix: "http",
		RouteSpecifier: &hcm.HttpConnectionManager_Rds{
			Rds: &hcm.Rds{
				ConfigSource:    makeConfigSource(),
				RouteConfigName: mlistener.uuid + "-route",
			},
		},
		HttpFilters: []*hcm.HttpFilter{{
			Name:       "http-router",
			ConfigType: &hcm.HttpFilter_TypedConfig{TypedConfig: routerConfig},
		}},
	}
	pbst, err := anypb.New(manager)
	if err != nil {
		panic(err)
	}

	original_src := &original_source.OriginalSrc{
		Mark: 123,
	}
	originalsrcpb, err := anypb.New(original_src)
	if err != nil {
		panic(err)
	}

	return &listener.Listener{
		Name: mlistener.uuid,
		Address: &core.Address{
			Address: &core.Address_SocketAddress{
				SocketAddress: &core.SocketAddress{
					Protocol: core.SocketAddress_TCP,
					Address:  mlistener.ip,
					PortSpecifier: &core.SocketAddress_PortValue{
						PortValue: uint32(mlistener.port),
					},
				},
			},
		},
		FilterChains: []*listener.FilterChain{{
			Filters: []*listener.Filter{{
				Name: mlistener.uuid + "-http-connection-manager",
				ConfigType: &listener.Filter_TypedConfig{
					TypedConfig: pbst,
				},
			}},
		}},
		ListenerFilters: []*listener.ListenerFilter{{
			Name: mlistener.uuid + "-envoy.filters.listener.original_src",
			ConfigType: &listener.ListenerFilter_TypedConfig{
				TypedConfig: originalsrcpb,
			},
		}},
	}
}

func createTlsListener(mlistener Listener, routes []Route) *listener.Listener {
	tls_inspectora := &tls_inspector.TlsInspector{}
	tls_inspectorpb, err := anypb.New(tls_inspectora)
	if err != nil {
		panic(err)
	}

	original_src := &original_source.OriginalSrc{
		Mark: 123,
	}
	originalsrcpb, err := anypb.New(original_src)
	if err != nil {
		panic(err)
	}

	var filterChain []*listener.FilterChain
	for _, route := range routes {
		if route.listener != mlistener.uuid {
			continue
		}

		var tcpProxy = &tcp_proxy.TcpProxy{
			StatPrefix: "tls",
			ClusterSpecifier: &tcp_proxy.TcpProxy_Cluster{
				Cluster: route.uuid,
			},
		}
		tcpProxypb, err := anypb.New(tcpProxy)
		if err != nil {
			panic(err)
		}

		filterChain = append(filterChain, &listener.FilterChain{
			Name: route.uuid,
			FilterChainMatch: &listener.FilterChainMatch{
				ServerNames: route.domain_names,
			},
			Filters: []*listener.Filter{{
				Name: mlistener.uuid + "/" + route.uuid,
				ConfigType: &listener.Filter_TypedConfig{
					TypedConfig: tcpProxypb,
				},
			}},
		})
	}

	return &listener.Listener{
		Name: mlistener.uuid,
		Address: &core.Address{
			Address: &core.Address_SocketAddress{
				SocketAddress: &core.SocketAddress{
					Protocol: core.SocketAddress_TCP,
					Address:  mlistener.ip,
					PortSpecifier: &core.SocketAddress_PortValue{
						PortValue: uint32(mlistener.port),
					},
				},
			},
		},
		FilterChains: filterChain,
		ListenerFilters: []*listener.ListenerFilter{
			{
				Name: mlistener.uuid + "-tls_inspector",
				ConfigType: &listener.ListenerFilter_TypedConfig{
					TypedConfig: tls_inspectorpb,
				},
			},
			{
				Name: mlistener.uuid + "-envoy.filters.listener.original_src",
				ConfigType: &listener.ListenerFilter_TypedConfig{
					TypedConfig: originalsrcpb,
				},
			},
		},
	}
}

func makeListeners(listeners []Listener, routes []Route) []types.Resource {
	var envoyListeners []types.Resource

	for _, listener := range listeners {
		if listener.stype == "http" {
			envoyListeners = append(envoyListeners, createHttpListener(listener, routes))
		} else if listener.stype == "tls" {
			envoyListeners = append(envoyListeners, createTlsListener(listener, routes))
		}
	}

	return envoyListeners
}

func makeConfigSource() *core.ConfigSource {
	source := &core.ConfigSource{}
	source.ResourceApiVersion = resource.DefaultAPIVersion
	source.ConfigSourceSpecifier = &core.ConfigSource_ApiConfigSource{
		ApiConfigSource: &core.ApiConfigSource{
			TransportApiVersion:       resource.DefaultAPIVersion,
			ApiType:                   core.ApiConfigSource_GRPC,
			SetNodeOnFirstMessageOnly: true,
			GrpcServices: []*core.GrpcService{{
				TargetSpecifier: &core.GrpcService_EnvoyGrpc_{
					EnvoyGrpc: &core.GrpcService_EnvoyGrpc{ClusterName: "xds_cluster"},
				},
			}},
		},
	}
	return source
}

func GenerateSnapshot() *cache.Snapshot {
	listeners, routes, err := DownloadListeners(os.Getenv("DB_CONN"))
	if err != nil {
		panic(err)
	}

	snap, _ := cache.NewSnapshot("1",
		map[resource.Type][]types.Resource{
			resource.ClusterType:  makeClusters(routes),
			resource.RouteType:    makeRoutes(listeners, routes),
			resource.ListenerType: makeListeners(listeners, routes),
		},
	)
	return snap
}
