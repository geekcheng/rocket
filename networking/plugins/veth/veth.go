// Copyright 2015 CoreOS, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"runtime"

	"github.com/appc/spec/schema/types"
	"github.com/coreos/rocket/Godeps/_workspace/src/github.com/vishvananda/netlink"

	"github.com/coreos/rocket/networking/ipam"
	"github.com/coreos/rocket/networking/util"
)

func init() {
	// this ensures that main runs only on main thread (thread group leader).
	// since namespace ops (unshare, setns) are done for a single thread, we
	// must ensure that the goroutine does not jump from OS thread to thread
	runtime.LockOSThread()
}

func cmdAdd(contID, netns, netConf, ifName, args string) error {
	var hostVethName, contIPNet string

	cid, err := types.NewUUID(contID)
	if err != nil {
		return fmt.Errorf("error parsing ContainerID: %v", err)
	}

	conf := util.Net{}
	if err := util.LoadNet(netConf, &conf); err != nil {
		return fmt.Errorf("failed to load %q: %v", netConf, err)
	}

	ips, err := ipam.AllocPtP(*cid, netConf, ifName, args)
	if err != nil {
		return err
	}

	hostIP, contIP := ips[0], ips[1]

	err = util.WithNetNSPath(netns, func(hostNS *os.File) error {
		entropy := contID + ifName

		ipn := &net.IPNet{
			IP:   contIP,
			Mask: net.CIDRMask(31, 32),
		}

		hostVeth, contVeth, err := util.SetupVeth(entropy, ifName, ipn, hostNS)
		if err != nil {
			return err
		}

		for _, r := range conf.Routes {
			dst, err := util.ParseCIDR(r)
			if err != nil {
				return fmt.Errorf("failed to parse route %q: %v", r, err)
			}

			if err = util.AddRoute(dst, hostIP, contVeth); err != nil {
				return fmt.Errorf("failed to add route %q: %v", dst, err)
			}
		}

		hostVethName = hostVeth.Attrs().Name
		contIPNet = ipn.String()

		return nil
	})
	if err != nil {
		return err
	}

	// hostVeth moved namespaces and may have a new ifindex
	hostVeth, err := netlink.LinkByName(hostVethName)
	if err != nil {
		return fmt.Errorf("failed to lookup %q: %v", hostVethName, err)
	}

	ipn := &net.IPNet{
		IP:   hostIP,
		Mask: net.CIDRMask(31, 32),
	}
	addr := &netlink.Addr{IPNet: ipn, Label: ""}
	if err = netlink.AddrAdd(hostVeth, addr); err != nil {
		return fmt.Errorf("failed to add IP addr to veth: %v", err)
	}

	// dst happens to be the same as IP/net of host veth
	if err = util.AddHostRoute(ipn, nil, hostVeth); err != nil && !os.IsExist(err) {
		return fmt.Errorf("failed to add route on host: %v", err)
	}

	fmt.Print(contIPNet)

	return nil
}

func cmdDel(contID, netns, netConf, ifName, args string) error {
	return util.WithNetNSPath(netns, func(hostNS *os.File) error {
		return util.DelLinkByName(ifName)
	})
}

func main() {
	var err error

	cmd := os.Getenv("RKT_NETPLUGIN_COMMAND")
	contID := os.Getenv("RKT_NETPLUGIN_CONTID")
	netns := os.Getenv("RKT_NETPLUGIN_NETNS")
	args :=	os.Getenv("RKT_NETPLUGIN_ARGS")
	ifName := os.Getenv("RKT_NETPLUGIN_IFNAME")
	netConf := os.Getenv("RKT_NETPLUGIN_NETCONF")

	if cmd == "" || contID == "" || netns == "" || ifName == "" || netConf == "" {
		log.Printf("Required env variable missing")
		log.Print("Env: ", os.Environ())
		os.Exit(1)
	}

	switch cmd {
	case "ADD":
		err = cmdAdd(contID, netns, netConf, ifName, args)

	case "DEL":
		err = cmdDel(contID, netns, netConf, ifName, args)

	default:
		log.Printf("Unknown RKT_NETPLUGIN_COMMAND: %v", cmd)
		os.Exit(1)
	}

	if err != nil {
		log.Printf("%v: %v", os.Args[1], err)
		os.Exit(1)
	}
}
