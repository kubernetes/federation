/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package service

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/federation/apis/federation/v1beta1"
	fakefedclientset "k8s.io/kubernetes/federation/client/clientset_generated/federation_clientset/fake"
	"k8s.io/kubernetes/federation/pkg/dnsprovider/providers/google/clouddns" // Only for unit testing purposes.
	. "k8s.io/kubernetes/federation/pkg/federation-controller/util/test"
	"k8s.io/kubernetes/pkg/api/v1"
)

func TestServiceController_ensureDnsRecords(t *testing.T) {
	cluster1Name := "c1"
	cluster2Name := "c2"
	cluster1 := NewClusterWithRegionZone(cluster1Name, v1.ConditionTrue, "fooregion", "foozone")
	cluster2 := NewClusterWithRegionZone(cluster2Name, v1.ConditionTrue, "barregion", "barzone")
	globalDNSName := "servicename.servicenamespace.myfederation.svc.federation.example.com"
	fooRegionDNSName := "servicename.servicenamespace.myfederation.svc.fooregion.federation.example.com"
	fooZoneDNSName := "servicename.servicenamespace.myfederation.svc.foozone.fooregion.federation.example.com"
	barRegionDNSName := "servicename.servicenamespace.myfederation.svc.barregion.federation.example.com"
	barZoneDNSName := "servicename.servicenamespace.myfederation.svc.barzone.barregion.federation.example.com"

	tests := []struct {
		name          string
		service       v1.Service
		expected      []string
		serviceStatus v1.LoadBalancerStatus
	}{
		{
			name: "ServiceWithSingleLBIngress",
			service: v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "servicename",
					Namespace: "servicenamespace",
					Annotations: map[string]string{
						FederatedServiceIngressAnnotation: NewFederatedServiceIngress().
							AddEndpoints(cluster1Name, []string{"198.51.100.1"}).
							String()},
				},
			},
			serviceStatus: buildServiceStatus([][]string{{"198.51.100.1", ""}}),
			expected: []string{
				"example.com:" + globalDNSName + ":A:180:[198.51.100.1]",
				"example.com:" + fooRegionDNSName + ":A:180:[198.51.100.1]",
				"example.com:" + fooZoneDNSName + ":A:180:[198.51.100.1]",
				"example.com:" + barRegionDNSName + ":CNAME:180:[" + globalDNSName + "]",
				"example.com:" + barZoneDNSName + ":CNAME:180:[" + barRegionDNSName + "]",
			},
		},
		/*
			TODO: getResolvedEndpoints preforms DNS lookup.
			Mock and maybe look at error handling when some endpoints resolve, but also caching?
			{
				name: "withname",
				service: v1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name: "servicename",
						Namespace: "servicenamespace",
					},
				},
				serviceStatus: buildServiceStatus([][]string{{"", "randomstring.amazonelb.example.com"}}),
				expected: []string{
					"example.com:"+globalDNSName+":A:180:[198.51.100.1]",
					"example.com:"+fooRegionDNSName+":A:180:[198.51.100.1]",
					"example.com:"+fooZoneDNSName+":A:180:[198.51.100.1]",
				},
			},
		*/
		{
			name: "ServiceWithNoLBIngress",
			service: v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "servicename",
					Namespace: "servicenamespace",
				},
			},
			expected: []string{
				"example.com:" + fooRegionDNSName + ":CNAME:180:[" + globalDNSName + "]",
				"example.com:" + fooZoneDNSName + ":CNAME:180:[" + fooRegionDNSName + "]",
				"example.com:" + barRegionDNSName + ":CNAME:180:[" + globalDNSName + "]",
				"example.com:" + barZoneDNSName + ":CNAME:180:[" + barRegionDNSName + "]",
			},
		},
		{
			name: "ServiceWithMultipleLBIngress",
			service: v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "servicename",
					Namespace: "servicenamespace",
					Annotations: map[string]string{
						FederatedServiceIngressAnnotation: NewFederatedServiceIngress().
							AddEndpoints(cluster1Name, []string{"198.51.100.1"}).
							AddEndpoints(cluster2Name, []string{"198.51.200.1"}).
							String()},
				},
			},
			expected: []string{
				"example.com:" + globalDNSName + ":A:180:[198.51.100.1 198.51.200.1]",
				"example.com:" + fooRegionDNSName + ":A:180:[198.51.100.1]",
				"example.com:" + fooZoneDNSName + ":A:180:[198.51.100.1]",
				"example.com:" + barRegionDNSName + ":A:180:[198.51.200.1]",
				"example.com:" + barZoneDNSName + ":A:180:[198.51.200.1]",
			},
		},
		{
			name: "ServiceWithLBIngressAndServiceDeleted",
			service: v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "servicename",
					Namespace:         "servicenamespace",
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
					Annotations: map[string]string{
						FederatedServiceIngressAnnotation: NewFederatedServiceIngress().
							AddEndpoints(cluster1Name, []string{"198.51.100.1"}).
							String()},
				},
			},
			expected: []string{
				// TODO: Ideally we should expect that there are no DNS records when federated service is deleted. Need to remove these leaks in future
				"example.com:" + fooRegionDNSName + ":CNAME:180:[" + globalDNSName + "]",
				"example.com:" + fooZoneDNSName + ":CNAME:180:[" + fooRegionDNSName + "]",
				"example.com:" + barRegionDNSName + ":CNAME:180:[" + globalDNSName + "]",
				"example.com:" + barZoneDNSName + ":CNAME:180:[" + barRegionDNSName + "]",
			},
		},
		{
			name: "ServiceWithMultipleLBIngressAndOneLBIngressGettingRemoved",
			service: v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "servicename",
					Namespace: "servicenamespace",
					Annotations: map[string]string{
						FederatedServiceIngressAnnotation: NewFederatedServiceIngress().
							AddEndpoints(cluster1Name, []string{"198.51.100.1"}).
							AddEndpoints(cluster2Name, []string{"198.51.200.1"}).
							RemoveEndpoint(cluster2Name, "198.51.200.1").
							String()},
				},
			},
			expected: []string{
				"example.com:" + globalDNSName + ":A:180:[198.51.100.1]",
				"example.com:" + fooRegionDNSName + ":A:180:[198.51.100.1]",
				"example.com:" + fooZoneDNSName + ":A:180:[198.51.100.1]",
				"example.com:" + barRegionDNSName + ":CNAME:180:[" + globalDNSName + "]",
				"example.com:" + barZoneDNSName + ":CNAME:180:[" + barRegionDNSName + "]",
			},
		},
		{
			name: "ServiceWithMultipleLBIngressAndAllLBIngressGettingRemoved",
			service: v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "servicename",
					Namespace: "servicenamespace",
					Annotations: map[string]string{
						FederatedServiceIngressAnnotation: NewFederatedServiceIngress().
							AddEndpoints(cluster1Name, []string{"198.51.100.1"}).
							AddEndpoints(cluster2Name, []string{"198.51.200.1"}).
							RemoveEndpoint(cluster1Name, "198.51.100.1").
							RemoveEndpoint(cluster2Name, "198.51.200.1").
							String()},
				},
			},
			expected: []string{
				"example.com:" + fooRegionDNSName + ":CNAME:180:[" + globalDNSName + "]",
				"example.com:" + fooZoneDNSName + ":CNAME:180:[" + fooRegionDNSName + "]",
				"example.com:" + barRegionDNSName + ":CNAME:180:[" + globalDNSName + "]",
				"example.com:" + barZoneDNSName + ":CNAME:180:[" + barRegionDNSName + "]",
			},
		},
	}
	for _, test := range tests {
		fakedns, _ := clouddns.NewFakeInterface()
		fakednsZones, ok := fakedns.Zones()
		if !ok {
			t.Error("Unable to fetch zones")
		}
		fakeClient := &fakefedclientset.Clientset{}
		RegisterFakeClusterGet(&fakeClient.Fake, &v1beta1.ClusterList{Items: []v1beta1.Cluster{*cluster1, *cluster2}})
		serviceController := ServiceController{
			federationClient: fakeClient,
			dns:              fakedns,
			dnsZones:         fakednsZones,
			serviceDnsSuffix: "federation.example.com",
			zoneName:         "example.com",
			federationName:   "myfederation",
			serviceCache:     &serviceCache{fedServiceMap: make(map[string]*cachedService)},
			clusterCache: &clusterClientCache{
				rwlock:    sync.Mutex{},
				clientMap: make(map[string]*clusterCache),
			},
			knownClusterSet: make(sets.String),
		}

		serviceController.clusterCache.clientMap[cluster1Name] = &clusterCache{
			cluster: &v1beta1.Cluster{
				Status: v1beta1.ClusterStatus{
					Zones:  []string{"foozone"},
					Region: "fooregion",
				},
			},
		}

		cachedService := &cachedService{
			lastState:        &test.service,
			endpointMap:      make(map[string]int),
			serviceStatusMap: make(map[string]v1.LoadBalancerStatus),
		}
		cachedService.endpointMap[cluster1Name] = 1
		if !reflect.DeepEqual(&test.serviceStatus, &v1.LoadBalancerStatus{}) {
			cachedService.serviceStatusMap[cluster1Name] = test.serviceStatus
		}

		err := serviceController.ensureDnsRecords(cluster1Name, &test.service)
		if err != nil {
			t.Errorf("Test failed for %s, unexpected error %v", test.name, err)
		}
		err = serviceController.ensureDnsRecords(cluster2Name, &test.service)
		if err != nil {
			t.Errorf("Test failed for %s, unexpected error %v", test.name, err)
		}

		zones, err := fakednsZones.List()
		if err != nil {
			t.Errorf("error querying zones: %v", err)
		}

		// Dump every record to a testable-by-string-comparison form
		records := []string{}
		for _, z := range zones {
			zoneName := z.Name()

			rrs, ok := z.ResourceRecordSets()
			if !ok {
				t.Errorf("cannot get rrs for zone %q", zoneName)
			}

			rrList, err := rrs.List()
			if err != nil {
				t.Errorf("error querying rr for zone %q: %v", zoneName, err)
			}
			for _, rr := range rrList {
				rrdatas := rr.Rrdatas()

				// Put in consistent (testable-by-string-comparison) order
				sort.Strings(rrdatas)
				records = append(records, fmt.Sprintf("%s:%s:%s:%d:%s", zoneName, rr.Name(), rr.Type(), rr.Ttl(), rrdatas))
			}
		}

		// Ignore order of records
		sort.Strings(records)
		sort.Strings(test.expected)

		if !reflect.DeepEqual(records, test.expected) {
			t.Errorf("Test %q failed.  Actual=%v, Expected=%v", test.name, records, test.expected)
		}

	}
}
