// Copyright © 2017-2019 Weald Technology Trading
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

package cmd

import (
	"bytes"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/spf13/cobra"
	"github.com/wealdtech/ethereal/cli"
	ens "github.com/wealdtech/go-ens/v3"
	string2eth "github.com/wealdtech/go-string2eth"
)

var zero = big.NewInt(0)

// ensInfoCmd represents the ens info command
var ensInfoCmd = &cobra.Command{
	Use:   "info",
	Short: "Obtain information about an ENS domain",
	Long: `Obtain information about a domain registered with the Ethereum Name Service (ENS).  For example:

    ens info --domain=enstest.eth

In quiet mode this will return 0 if the domain is owned, otherwise 1.`,

	Run: func(cmd *cobra.Command, args []string) {
		cli.Assert(ensDomain != "", quiet, "--domain is required")

		ensDomain, err := ens.NormaliseDomain(ensDomain)
		cli.ErrCheck(err, quiet, "Failed to normalise ENS domain")

		// Domain information
		outputIf(verbose, fmt.Sprintf("Normalised domain is %s", ensDomain))
		outputIf(verbose, fmt.Sprintf("Top-level domain is %s", ens.Tld(ensDomain)))
		outputIf(verbose, fmt.Sprintf("Domain level is %v", ens.DomainLevel(ensDomain)))
		nameHash, err := ens.NameHash(ensDomain)
		cli.ErrCheck(err, quiet, "Failed to obtain name hash of ENS domain")
		outputIf(verbose, fmt.Sprintf("Name hash is 0x%x", nameHash))
		label, _ := ens.DomainPart(ensDomain, 1)
		outputIf(verbose, fmt.Sprintf("Label is %s", label))
		labelHash, err := ens.LabelHash(label)
		cli.ErrCheck(err, quiet, "Failed to obtain label hash of ENS domain")
		outputIf(verbose, fmt.Sprintf("Label hash of %s is 0x%x", label, labelHash))

		if ens.DomainLevel(ensDomain) == 1 && ens.Tld(ensDomain) == "eth" {
			// Work out if this is on the old or new .eth registrar and act accordingly
			registrar, err := ens.NewBaseRegistrar(client, ens.Tld(ensDomain))
			cli.ErrCheck(err, quiet, fmt.Sprintf("Failed to obtain ENS registrar contract for %s", ens.Tld(ensDomain)))
			outputIf(debug, fmt.Sprintf("Registrar address is %#x", registrar.ContractAddr))

			domain, err := ens.DomainPart(ensDomain, 1)
			registrant, err := registrar.Owner(domain)
			if err != nil {
				if err.Error() == "abi: attempting to unmarshall an empty string while arguments are expected" {
					fmt.Println("Name not recognised by registrar")
					os.Exit(_exit_failure)
				} else {
					cli.ErrCheck(err, quiet, "Failed to obtain registrant")
				}
			}
			if registrant == ens.UnknownAddress {
				fmt.Println("Name not recognised by registrar")
				unregisteredResolverCheck(ensDomain)
				os.Exit(_exit_failure)
			}

			outputIf(verbose, fmt.Sprintf("Registrar is %s", ens.Format(client, registrar.ContractAddr)))
			registrantName, _ := ens.ReverseResolve(client, registrant)
			if registrantName == "" {
				fmt.Printf("Registrant is %s\n", registrant.Hex())
			} else {
				fmt.Printf("Registrant is %s (%s)\n", registrantName, registrant.Hex())
			}
			expiry, err := registrar.Expiry(domain)
			cli.ErrCheck(err, quiet, "Failed to obtain expiry")
			fmt.Printf("Registration expires at %v\n", time.Unix(int64(expiry.Uint64()), 0))

			controller, err := ens.NewETHController(client, ens.Domain(ensDomain))
			cli.ErrCheck(err, quiet, "Failed to obtain controller")
			rentPerSec, err := controller.RentCost(ensDomain)
			if err == nil {
				// Select (approximate) cost per year
				rentPerYear := new(big.Int).Mul(big.NewInt(31536000), rentPerSec)
				fmt.Printf("Approximate rent per year is %s\n", string2eth.WeiToString(rentPerYear, true))
			}

			// See if there is an outstanding deed.
			auctionRegistrarAddress := common.HexToAddress("0x6090A6e47849629b7245Dfa1Ca21D94cd15878Ef")
			auctionRegistrar, err := ens.NewAuctionRegistrarAt(client, ens.Tld(ensDomain), auctionRegistrarAddress)
			cli.ErrCheck(err, quiet, "Cannot obtain ENS auction registrar contract")
			entry, err := auctionRegistrar.Entry(ensDomain)
			if err == nil && entry != nil && entry.Deed != ens.UnknownAddress {
				if entry.Value.Cmp(zero) == 0 {
					entry.Value, _ = string2eth.StringToWei("0.01 ether")
				}
				fmt.Printf("Deed value is %s; release with 'ethereal ens release'\n", string2eth.WeiToString(entry.Value, true))
			}
			genericInfo(ensDomain)
		}
	},
}

func init() {
	ensCmd.AddCommand(ensInfoCmd)
	ensFlags(ensInfoCmd)
}

func availableInfo(name string) {
	if len(name) < 11 { // 7 + 4 for '.eth'
		fmt.Println("Unavailable due to name length restrictions")
	} else {
		fmt.Println("Available")
	}
}

func biddingInfo(registrar *ens.AuctionRegistrar, name string) {
	entry, err := registrar.Entry(name)
	cli.ErrCheck(err, quiet, "Cannot obtain information for that name")

	twoDaysAgo := time.Duration(-48) * time.Hour
	fmt.Println("Bidding until", entry.Registration.Add(twoDaysAgo))
}

func revealingInfo(registrar *ens.AuctionRegistrar, name string) {
	entry, err := registrar.Entry(name)
	cli.ErrCheck(err, quiet, "Cannot obtain information for that name")

	fmt.Println("Revealing until", entry.Registration)
	// If the value is 0 then it is is minvalue instead
	if entry.Value.Cmp(zero) == 0 {
		entry.Value, _ = string2eth.StringToWei("0.01 ether")
	}
	fmt.Println("Locked value is", string2eth.WeiToString(entry.Value, true))
	fmt.Println("Highest bid is", string2eth.WeiToString(entry.HighestBid, true))
}

func wonInfo(registrar *ens.AuctionRegistrar, name string) {
	entry, err := registrar.Entry(name)
	cli.ErrCheck(err, quiet, "Cannot obtain information for that name")

	fmt.Println("Won since", entry.Registration)
	if entry.Value.Cmp(zero) == 0 {
		entry.Value, _ = string2eth.StringToWei("0.01 ether")
	}
	fmt.Println("Locked value is", string2eth.WeiToString(entry.Value, true))
	fmt.Println("Highest bid was", string2eth.WeiToString(entry.HighestBid, true))

	// Deed
	deed, err := ens.NewDeedAt(client, entry.Deed)
	cli.ErrCheck(err, quiet, "Failed to obtain deed contract")
	// Registrant
	registrant, err := deed.Owner()
	cli.ErrCheck(err, quiet, "Failed to obtain registrant")
	registrantName, _ := ens.ReverseResolve(client, registrant)
	if registrantName == "" {
		fmt.Println("Registrant is", registrant.Hex())
	} else {
		fmt.Printf("Registrant is %s (%s)\n", registrantName, registrant.Hex())
	}
}

func ownedInfo(registrar *ens.AuctionRegistrar, name string) {
	entry, err := registrar.Entry(name)
	if err == nil {
		fmt.Println("Registered since", entry.Registration)
		fmt.Println("Locked value is", string2eth.WeiToString(entry.Value, true))
		fmt.Println("Highest bid was", string2eth.WeiToString(entry.HighestBid, true))

		// Deed
		deed, err := ens.NewDeedAt(client, entry.Deed)
		cli.ErrCheck(err, quiet, "Failed to obtain deed contract")
		// Registrant
		registrant, err := deed.Owner()
		cli.ErrCheck(err, quiet, "Failed to obtain registrant")
		registrantName, _ := ens.ReverseResolve(client, registrant)
		if registrantName == "" {
			fmt.Println("Registrant is", registrant.Hex())
		} else {
			fmt.Printf("Registrant is %s (%s)\n", registrantName, registrant.Hex())
		}
		previousRegistrant, err := deed.PreviousOwner()
		cli.ErrCheck(err, quiet, "Failed to obtain previous registrant")
		if bytes.Compare(previousRegistrant.Bytes(), ens.UnknownAddress.Bytes()) != 0 {
			previousRegistrantName, _ := ens.ReverseResolve(client, previousRegistrant)
			if previousRegistrantName == "" {
				fmt.Println("Previous registrant is", previousRegistrant.Hex())
			} else {
				fmt.Printf("Previous registrant is %s (%s)\n", previousRegistrantName, previousRegistrant.Hex())
			}
		}
	}
}

// It is possible for an unregistered domain to have a resolver; report if this is the case
func unregisteredResolverCheck(domain string) {
	registry, err := ens.NewRegistry(client)
	cli.ErrCheck(err, quiet, "Failed to obtain registry contract")
	resolverAddress, err := registry.ResolverAddress(domain)
	if err != nil {
		return
	}
	if resolverAddress != ens.UnknownAddress {
		fmt.Println(`                            *********************
                            ***    WARNING    ***
                            *********************
This domain is not registered but has a configured resolver.  This can occur
when a previously-configured domain expires or is released.  ENS will continue
to resolve addresses for this domain but the results should not be trusted as
a malicious part could register the domain and change the resolution.`)
	}
}

// genericInfo prints generic info about any ENS domain.
// It returns true if the domain exists, otherwise false
func genericInfo(name string) bool {
	registry, err := ens.NewRegistry(client)
	cli.ErrCheck(err, quiet, "Failed to obtain registry contract")
	controllerAddress, err := registry.Owner(ensDomain)
	cli.ErrCheck(err, quiet, "Failed to obtain controller")
	if controllerAddress == ens.UnknownAddress {
		fmt.Println("Owner not set")
		return false
	}
	controllerName, _ := ens.ReverseResolve(client, controllerAddress)
	if controllerName == "" {
		fmt.Printf("Controller is %s\n", controllerAddress.Hex())
	} else {
		fmt.Printf("Controller is %s (%s)\n", controllerName, controllerAddress.Hex())
	}

	// Resolver
	resolverAddress, err := registry.ResolverAddress(name)
	if err != nil || resolverAddress == ens.UnknownAddress {
		fmt.Println("Resolver not configured")
		return true
	}
	resolverName, _ := ens.ReverseResolve(client, resolverAddress)
	if resolverName == "" {
		fmt.Printf("Resolver is %s\n", resolverAddress.Hex())
	} else {
		fmt.Printf("Resolver is %s (%s)\n", resolverName, resolverAddress.Hex())
	}

	// Address
	address, err := ens.Resolve(client, name)
	if err == nil && address != ens.UnknownAddress {
		fmt.Printf("Domain resolves to %s\n", address.Hex())
		// Reverse resolution
		reverseDomain, err := ens.ReverseResolve(client, address)
		if err == nil && reverseDomain != "" {
			fmt.Printf("Address resolves to %s\n", reverseDomain)
		}
	}

	// Content hash
	resolver, err := ens.NewResolverAt(client, name, resolverAddress)
	if err == nil {
		bytes, err := resolver.Contenthash()
		if err == nil && len(bytes) > 0 {
			contentHash, err := ens.ContenthashToString(bytes)
			if err == nil {
				fmt.Printf("Content hash is %v\n", contentHash)
			}
		}
	}

	return true
}
