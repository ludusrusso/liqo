package reflectors

import (
	apimgmt "github.com/liqotech/liqo/pkg/virtualKubelet/apiReflection"
)

type blackList map[string]struct{}

var Blacklist = map[apimgmt.ApiType]blackList{
	apimgmt.EndpointSlices: {
		"default/kubernetes": struct{}{},
	},
	apimgmt.Pods: {},
	apimgmt.Services: {
		"default/kubernetes": struct{}{},
	},
}
