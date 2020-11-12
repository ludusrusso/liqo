package wireguard

import (
	"fmt"
	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"k8s.io/klog/v2"
	"net"
	"strconv"
	"time"
)

const (
	wgLinkType = "wireguard"
	PublicKey  = "publicKey"  // PublicKey is the key of publicKey entry in back-end map and also for the secret containing the wireguard keys
	PrivateKey = "privateKey" // PrivateKey is the key of private for the secret containing the wireguard keys
)

type WgConfig struct {
	Name   string       //device name
	Port   *int         //listening port
	PriKey *wgtypes.Key //private key
	PubKey *wgtypes.Key //public key
}

type Wireguard struct {
	client *wgctrl.Client // client used to interact with the wireguard implementation in use
	link   netlink.Link   // the wireguard link living in the network namespace
	conf   WgConfig       // wireguard configuration
}

func NewWireguard(config WgConfig) (*Wireguard, error) {
	var err error
	w := &Wireguard{
		conf: config,
	}
	// create and set up the interface
	if err = w.setWGLink(config.Name); err != nil {
		return nil, err
	}
	// create a wireguard client
	if w.client, err = wgctrl.New(); err != nil {
		return nil, fmt.Errorf("unable to create wireguard client: %v", err)
	}
	// if something goes wrong we make sure to close the client connection
	defer func() {
		if err != nil {
			if e := w.client.Close(); e != nil {
				klog.Errorf("Failed to close client %v", e)
			}
			w.client = nil
		}
	}()
	//configures the device
	peerConfigs := make([]wgtypes.PeerConfig, 0)
	cfg := wgtypes.Config{
		PrivateKey:   config.PriKey,
		ListenPort:   config.Port,
		FirewallMark: nil,
		ReplacePeers: true,
		Peers:        peerConfigs,
	}
	if err = w.client.ConfigureDevice(config.Name, cfg); err != nil {
		return nil, err
	}
	return w, nil
}

// it adds a new peer with the given configuration to the wireguard device
func (w *Wireguard) AddPeer(pubkey, endpointIP, listeningPort string, allowedIPs []string, keepAlive *time.Duration) error {
	key, err := wgtypes.ParseKey(pubkey)
	if err != nil {
		return err
	}
	epIP := net.ParseIP(endpointIP)
	//convert port from string to int
	port, err := strconv.ParseInt(listeningPort, 10, 0)
	if err != nil {
		return err
	}
	var IPs []net.IPNet
	for _, subnet := range allowedIPs {
		_, s, err := net.ParseCIDR(subnet)
		if err != nil {
			return err
		}
		IPs = append(IPs, *s)
	}
	err = w.client.ConfigureDevice(w.GetDeviceName(), wgtypes.Config{
		ReplacePeers: false,
		Peers: []wgtypes.PeerConfig{{
			PublicKey:    key,
			Remove:       false,
			UpdateOnly:   false,
			PresharedKey: nil,
			Endpoint: &net.UDPAddr{
				IP:   epIP,
				Port: int(port),
			},
			PersistentKeepaliveInterval: keepAlive,
			ReplaceAllowedIPs:           true,
			AllowedIPs:                  IPs,
		}},
	})
	if err != nil {
		return err
	}
	return nil
}

// it removes a peer with a given public key from the wireguard device
func (w *Wireguard) removePeer(pubKey string) error {
	key, err := wgtypes.ParseKey(pubKey)
	if err != nil {
		return err
	}
	peerCfg := []wgtypes.PeerConfig{
		{
			PublicKey: key,
			Remove:    true,
		},
	}
	err = w.client.ConfigureDevice(w.GetDeviceName(), wgtypes.Config{
		ReplacePeers: false,
		Peers:        peerCfg,
	})
	if err != nil {
		return err
	}
	return nil
}

// returns all the peers configured for the given wireguard device
func (w *Wireguard) getPeers() ([]wgtypes.Peer, error) {
	d, err := w.client.Device(w.GetDeviceName())
	if err != nil {
		return nil, err
	}
	return d.Peers, nil
}

// given a public key it returns the peer which has the same key
func (w *Wireguard) getPeer(pubKey string) (wgtypes.Peer, error) {
	var peer wgtypes.Peer
	peers, err := w.getPeers()
	if err != nil {
		return peer, err
	}
	for _, p := range peers {
		if p.PublicKey.String() == pubKey {
			return p, nil
		}
	}
	return peer, fmt.Errorf("peer with public key '%s' not found for wireguard device '%s'", pubKey, w.GetDeviceName())
}

// get name of the wireguard device
func (w *Wireguard) GetDeviceName() string {
	return w.conf.Name
}

// get public key of the wireguard device
func (w *Wireguard) GetPubKey() string {
	return w.conf.PubKey.String()
}

// Create new wg link and sets it up and running
func (w *Wireguard) setWGLink(deviceName string) error {
	// delete existing wg device if needed
	if link, err := netlink.LinkByName(deviceName); err == nil {
		// delete existing device
		if err := netlink.LinkDel(link); err != nil {
			return fmt.Errorf("failed to delete existing wireguard device '%s': %v", deviceName, err)
		}
	}
	// create the wg device (ip link add dev $DefaultDeviceName type wireguard)
	la := netlink.NewLinkAttrs()
	la.Name = deviceName
	link := &netlink.GenericLink{
		LinkAttrs: la,
		LinkType:  wgLinkType,
	}
	if err := netlink.LinkAdd(link); err == nil {
		w.link = link
	} else {
		return fmt.Errorf("failed to add wireguard device '%s': %v", deviceName, err)
	}

	// ip link set $w.getName up
	if err := netlink.LinkSetUp(w.link); err != nil {
		return fmt.Errorf("failed to bring up wireguard device '%s': %v", deviceName, err)
	}
	return nil
}

//adds the ip address to the interface
//ip addres in cidr notation: x.x.x.x/x
func (wg *Wireguard) AddIP(ipAddr string) error {
	ipNet, err := netlink.ParseIPNet(ipAddr)
	if err != nil {
		return err
	}
	err = netlink.AddrAdd(wg.link, &netlink.Addr{IPNet: ipNet})
	if err != nil {
		return err
	}
	return nil
}
