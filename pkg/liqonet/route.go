package liqonet

import (
	"fmt"
	netv1alpha1 "github.com/liqotech/liqo/apis/net/v1alpha1"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"net"
)

type NetLink interface {
	AddRoute(dst string, gw string, deviceName string, onLink bool) (netlink.Route, error)
	DelRoute(route netlink.Route) error
}

type RouteManager struct {
}

func (r *RouteManager) EnsureRoutesPerCluster(interfaceName string, tep *netv1alpha1.TunnelEndpoint) error {
	_, remotePodCIDR := getPodCIDRS(tep)

	_, err := r.AddRoute(remotePodCIDR, "", interfaceName, false)
	if err == unix.EEXIST {
		return nil
	}
	if err != nil{
		return err
	}
	return nil
}

//used to remove the routes when a tunnelEndpoint CR is removed
func (r *RouteManager) removeRoutesPerCluster(tep *netv1alpha1.TunnelEndpoint) error {
	_, remotePodCIDR := getPodCIDRS(tep)
	ip, _, err := net.ParseCIDR(remotePodCIDR)
	if err != nil{
		return err
	}
	routes, err := netlink.RouteGet(ip)
	if err != nil {
		return err
	}
	for _, route := range routes{
		err := r.DelRoute(route)
		if err != nil {
			return err
		}
	}
	return nil
}

func (rm *RouteManager) AddRoute(dst string, gw string, deviceName string, onLink bool) (netlink.Route, error) {
	var route netlink.Route
	//convert destination in *net.IPNet
	_, destinationNet, err := net.ParseCIDR(dst)
	if err != nil {
		return route, fmt.Errorf("unable to convert destination \"%s\" from string to net.IPNet: %v", dst, err)
	}
	gateway := net.ParseIP(gw)
	iface, err := netlink.LinkByName(deviceName)
	if err != nil {
		return route, fmt.Errorf("unable to retrieve information of \"%s\": %v", deviceName, err)
	}
	if onLink {
		route = netlink.Route{LinkIndex: iface.Attrs().Index, Dst: destinationNet, Gw: gateway, Flags: unix.RTNH_F_ONLINK}

		if err := netlink.RouteAdd(&route); err != nil {
			return route, fmt.Errorf("unable to instantiate route for %s  network with gateway %s:%v", dst, gw, err)
		}
	} else {
		route = netlink.Route{LinkIndex: iface.Attrs().Index, Dst: destinationNet, Gw: gateway}
		if err := netlink.RouteAdd(&route); err != nil {
			return route, fmt.Errorf("unable to instantiate route for %s  network with gateway %s:%v", dst, gw, err)
		}
	}
	return route, nil
}

func IsRouteConfigTheSame(existing *netlink.Route, new netlink.Route) bool {
	if existing.LinkIndex == new.LinkIndex && existing.Gw.String() == new.Gw.String() && existing.Dst.String() == new.Dst.String() {
		return true
	} else {
		return false
	}
}

func (rm *RouteManager) DelRoute(route netlink.Route) error {
	//try to remove all the routes for that ip
	err := netlink.RouteDel(&route)
	if err != nil {
		if err == unix.ESRCH {
			//it means the route does not exist so we are done
			return nil
		}
		return err
	}
	return nil
}
