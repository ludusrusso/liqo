package liqonet

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vishvananda/netlink"
	"io/ioutil"
	"net"
	"os"
	"strconv"
	"strings"
)

const (
	vxlanOverhead int = 50
)

type VxlanNetConfig struct {
	NetworkPrefix string `json:"NetworkPrefix"`
	DeviceName    string `json:"DeviceName"`
	Port          string `json:"Port"`
	Vni           string `json:"Vni"`
}

func CreateVxLANInterface(vxlanConfig VxlanNetConfig)( *VxlanDevice, error) {
	podIPAddr, err := GetPodIP()
	if err != nil {
		return nil, err
	}
	//get the mtu of the default interface
	mtu, err := getDefaultIfaceMTU()
	if err != nil {
		return nil, err
	}

	//derive IP for the vxlan device
	//take the last octet of the podIP
	//TODO: use & and | operators with masks
	temp := strings.Split(podIPAddr.String(), ".")
	vxlanIPString := vxlanConfig.NetworkPrefix + "." + temp[1] + "." + temp[2] + "." + temp[3]
	vxlanIP := net.ParseIP(vxlanIPString)

	vxlanMTU := mtu - vxlanOverhead
	vni, err := strconv.Atoi(vxlanConfig.Vni)
	if err != nil {
		return nil, fmt.Errorf("unable to convert vxlan vni \"%s\" from string to int: %v", vxlanConfig.Vni, err)
	}
	port, err := strconv.Atoi(vxlanConfig.Port)
	if err != nil {
		return nil, fmt.Errorf("unable to convert vxlan port \"%s\" from string to int: %v", vxlanConfig.Port, err)
	}
	attr := &VxlanDeviceAttrs{
		Vni:      uint32(vni),
		Name:     vxlanConfig.DeviceName,
		VtepPort: port,
		VtepAddr: podIPAddr,
		Mtu:      vxlanMTU,
	}
	vxlanDev, err := NewVXLANDevice(attr)
	if err != nil {
		return nil, fmt.Errorf("failed to create vxlan interface on node with ip -> %s: %v", podIPAddr.String(), err)
	}
	err = vxlanDev.ConfigureIPAddress(vxlanIP, net.IPv4Mask(240, 0, 0, 0))
	if err != nil {
		return nil, fmt.Errorf("failed to configure ip in vxlan interface on node with ip -> %s: %v", podIPAddr.String(), err)
	}
	if err = vxlanDev.EnableRPFilter(); err != nil{
			return nil, err
		}
	return vxlanDev, nil
}



func getDefaultIfaceMTU() (int, error) {
	//search for the default route and return the link associated to the route
	//we consider only the ipv4 routes
	mtu := 0
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return mtu, fmt.Errorf("unable to list routes while trying to identify default interface for the host: %v", err)
	}
	var route netlink.Route
	for _, route = range routes {
		if route.Dst == nil {
			break
		}
	}
	//get default link
	defualtIface, err := netlink.LinkByIndex(route.LinkIndex)
	if err != nil {
		return mtu, fmt.Errorf("unable to retrieve link with index %d :%v", route.LinkIndex, err)
	}
	return defualtIface.Attrs().MTU, nil
}

//the config file is expected to reside in /etc/kube-liqo/liqonet/vxlan-net-conf.json
func ReadVxlanNetConfig(defaultConfig VxlanNetConfig) (VxlanNetConfig, error) {
	pathToConfigFile := "/etc/kube-liqonet/liqonet/vxlan-net-conf.json" //path where we expect the configuration file

	var config VxlanNetConfig
	//check if the file exists
	if _, err := os.Stat(pathToConfigFile); err == nil {
		data, err := ioutil.ReadFile(pathToConfigFile)
		//TODO: add debugging info
		if err != nil {
			return config, fmt.Errorf("an erro occured while reading \"%s\" configuration file: %v", pathToConfigFile, err)
		}
		err = json.Unmarshal(data, &config)
		if err != nil {
			return config, fmt.Errorf("an error occured while unmarshalling \"%s\" configuration file: %v", pathToConfigFile, err)
		}
		if config.NetworkPrefix == "" || config.Port == "" || config.DeviceName == "" || config.Vni == "" {
			return config, errors.New("some configuration fields are missing in \"" + pathToConfigFile + "\", please check your configuration.")
		}
		return config, nil
	} else {
		return defaultConfig, nil
	}
}
