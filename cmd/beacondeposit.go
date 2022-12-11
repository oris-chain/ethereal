// Copyright © 2017-2022 Weald Technology Trading
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
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/wealdtech/ethereal/v2/cli"
	"github.com/wealdtech/ethereal/v2/conn"
	"github.com/wealdtech/ethereal/v2/util"
	"github.com/wealdtech/ethereal/v2/util/contracts"
	ens "github.com/wealdtech/go-ens/v3"
	string2eth "github.com/wealdtech/go-string2eth"
)

// depositABI contains the ABI for the deposit contract.
var depositABI = `[{"inputs":[{"internalType":"bytes","name":"pubkey","type":"bytes"},{"internalType":"bytes","name":"withdrawal_credentials","type":"bytes"},{"internalType":"bytes","name":"signature","type":"bytes"},{"internalType":"bytes32","name":"deposit_data_root","type":"bytes32"}],"name":"deposit","outputs":[],"stateMutability":"payable","type":"function"}]`

var beaconDepositData string
var beaconDepositFrom string
var beaconDepositAllowOldData bool
var beaconDepositAllowNewData bool
var beaconDepositAllowExcessiveDeposit bool
var beaconDepositAllowUnknownContract bool
var beaconDepositAllowDuplicateDeposit bool
var beaconDepositForceZeroValue bool
var beaconDepositContractAddress string
var beaconDepositEth2Network string
var beaconDepositOverrideGas uint64

type beaconDepositContract struct {
	network     string
	chainID     *big.Int
	address     []byte
	forkVersion []byte
	minVersion  uint64
	maxVersion  uint64
	subgraph    string
}

var beaconDepositKnownContracts = []*beaconDepositContract{
	{
		network:     "Mainnet",
		chainID:     big.NewInt(1),
		address:     util.MustDecodeHexString("0x00000000219ab540356cBB839Cbe05303d7705Fa"),
		forkVersion: []byte{0x00, 0x00, 0x00, 0x00},
		minVersion:  3,
		maxVersion:  4,
		subgraph:    "attestantio/eth2deposits",
	},
	{
		network:     "Ropsten",
		chainID:     big.NewInt(3),
		address:     util.MustDecodeHexString("0x6f22fFbC56eFF051aECF839396DD1eD9aD6BBA9D"),
		forkVersion: []byte{0x80, 0x00, 0x00, 0x69},
		minVersion:  3,
		maxVersion:  4,
	},
	{
		network:     "Prater",
		chainID:     big.NewInt(5),
		address:     util.MustDecodeHexString("0xff50ed3d0ec03aC01D4C79aAd74928BFF48a7b2b"),
		forkVersion: []byte{0x00, 0x00, 0x10, 0x20},
		minVersion:  3,
		maxVersion:  4,
		subgraph:    "attestantio/eth2deposits-prater",
	},
	{
		network:     "Kiln",
		chainID:     big.NewInt(1337802),
		address:     util.MustDecodeHexString("0x4242424242424242424242424242424242424242"),
		forkVersion: []byte{0x70, 0x00, 0x00, 0x69},
		minVersion:  3,
		maxVersion:  4,
	},
	{
		network:     "Sepolia",
		chainID:     big.NewInt(11155111),
		address:     util.MustDecodeHexString("0x7f02C3E3c98b133055B8B348B2Ac625669Ed295D"),
		forkVersion: []byte{0x90, 0x00, 0x00, 0x69},
		minVersion:  3,
		maxVersion:  4,
	},
}

// beaconDepositCmd represents the beacon deposit command
var beaconDepositCmd = &cobra.Command{
	Use:   "deposit",
	Short: "Deposit Ether to the beacon contract.",
	Long: `Deposit Ether to the Ethereum 2 beacon contract, either creating or supplementing a validator.  For example:

    ethereal beacon deposit --data=/home/me/depositdata.json --from=0x.... --passphrase="my secret passphrase"

The depositdata.json file can be generated by ethdo.  The data can be an array of deposits, in which case they will be processed sequentially.

The keystore for the account that owns the name must be local (i.e. listed with 'get accounts list') and unlockable with the supplied passphrase.

This will return an exit status of 0 if the transaction is successfully submitted (and mined if --wait is supplied), 1 if the transaction is not successfully submitted, and 2 if the transaction is successfully submitted but not mined within the supplied time limit.`,
	Run: func(cmd *cobra.Command, args []string) {
		cli.Assert(beaconDepositData != "", quiet, "--data is required")
		depositInfo, err := loadDepositInfo(beaconDepositData)
		cli.ErrCheck(err, quiet, "failed to load deposit info")

		// Fetch the contract details.
		cli.Assert(beaconDepositContractAddress != "" || beaconDepositEth2Network != "", quiet, "one of --address or --eth2network is required")
		contract, err := fetchBeaconDepositContract(beaconDepositContractAddress, beaconDepositEth2Network)
		cli.ErrCheck(err, quiet, `Deposit contract is unknown.  This means you are either running an old version of ethereal, or are attempting to send to the wrong network or a custom contract.  You should confirm that you are on the latest version of Ethereal by comparing the output of running "ethereal version" with the release information at https://github.com/wealdtech/ethereal/releases and upgrading where appropriate.

If you are *completely sure* you know what you are doing, you can use the --allow-unknown-contract option to carry out this transaction.  Otherwise, please seek support to ensure you do not lose your Ether.`)
		outputIf(verbose && contractName != "", fmt.Sprintf("Deposit contract is %s", contract.network))

		cli.Assert(c.ChainID().Cmp(contract.chainID) == 0, quiet, "Ethereal is not connected to the correct Ethereum 1 network.  Please ensure that if you are depositing for the mainnet deposit contract you are on the Ethereum 1 mainnet, and likewise for test networks.")

		// Confirm the deposit data before sending any.
		for i := range depositInfo {
			cli.Assert(len(depositInfo[i].PublicKey) > 0, quiet, fmt.Sprintf("No public key for deposit %d", i))
			cli.Assert(len(depositInfo[i].DepositDataRoot) > 0, quiet, fmt.Sprintf("No data root for deposit %d", i))
			cli.Assert(len(depositInfo[i].Signature) > 0, quiet, fmt.Sprintf("No signature for deposit %d", i))
			cli.Assert(len(depositInfo[i].WithdrawalCredentials) > 0, quiet, fmt.Sprintf("No withdrawal credentials for deposit %d", i))
			if len(contract.forkVersion) != 0 && len(depositInfo[i].ForkVersion) != 0 {
				cli.Assert(bytes.Equal(depositInfo[i].ForkVersion, contract.forkVersion), quiet, fmt.Sprintf("Incorrect fork version for deposit %d (expected %#x, found %#x)", i, contract.forkVersion, depositInfo[i].ForkVersion))
			}
			cli.Assert(depositInfo[i].Amount >= 1000000000, quiet, fmt.Sprintf("Deposit too small for deposit %d", i))
			cli.Assert(depositInfo[i].Amount <= 32000000000 || beaconDepositAllowExcessiveDeposit, quiet, fmt.Sprintf(`Deposit more than 32 Ether for deposit %d.  Any amount above 32 Ether that is deposited will not count towards the validator's effective balance, and is effectively wasted.

If you really want to do this use the --allow-excessive-deposit option.`, i))

			cli.Assert(beaconDepositAllowOldData || depositInfo[i].Version >= contract.minVersion, quiet, `Data generated by ethdo is old and possibly inaccurate.  This means you need to upgrade your version of ethdo (or you are sending your deposit to the wrong contract or network); please do so by visiting https://github.com/wealdtech/ethdo and following the installation instructions there.  Once you have done this please regenerate your deposit data and try again.

If you are *completely sure* you know what you are doing, you can use the --allow-old-data option to carry out this transaction.  Otherwise, please seek support to ensure you do not lose your Ether.`)
			cli.Assert(beaconDepositAllowNewData || depositInfo[i].Version <= contract.maxVersion, quiet, `Data generated by ethdo is newer than supported.  This means you need to upgrade your version of ethereal (or you are sending your deposit to the wrong contract or network); please do so by visiting https://github.com/wealdtech/ethereal and following the installation instructions there.  Once you have done this please try again.

If you are *completely sure* you know what you are doing, you can use the --allow-new-data option to carry out this transaction.  Otherwise, please seek support to ensure you do not lose your Ether.`)
		}

		cli.Assert(beaconDepositFrom != "", quiet, "--from is required")
		fromAddress, err := resolveAddress(c.Client(), beaconDepositFrom)
		cli.ErrCheck(err, quiet, "Failed to obtain address for --from")

		if offline {
			sendOffline(c, depositInfo, contract, fromAddress)
		} else {
			sendOnline(depositInfo, contract, fromAddress)
		}
		os.Exit(exitSuccess)
	},
}

func resolveAddress(client *ethclient.Client, input string) (common.Address, error) {
	switch {
	case strings.Contains(input, "."):
		if client == nil {
			return common.Address{}, errors.New("cannot resolve ENS names when offline")
		}
		return ens.Resolve(client, input)
	case len(strings.TrimPrefix(input, "0x")) > 40:
		return common.Address{}, errors.New("address to long")
	default:
		address := common.HexToAddress(input)
		if address == ens.UnknownAddress {
			err = errors.New("could not parse address")
		}
		return address, nil
	}
}

func loadDepositInfo(input string) ([]*util.DepositInfo, error) {
	var err error
	var data []byte
	// Input could be JSON or a path to JSON
	switch {
	case strings.HasPrefix(input, "{"):
		// Looks like JSON
		data = []byte("[" + input + "]")
	case strings.HasPrefix(input, "["):
		// Looks like JSON array
		data = []byte(input)
	default:
		// Assume it's a path to JSON
		data, err = os.ReadFile(input)
		if err != nil {
			return nil, errors.Wrap(err, "failed to find deposit data file")
		}
		if data[0] == '{' {
			data = []byte("[" + string(data) + "]")
		}
	}

	depositInfo, err := util.DepositInfoFromJSON(data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to obtain deposit information")
	}
	if len(depositInfo) == 0 {
		return nil, errors.New("no deposit information supplied")
	}
	return depositInfo, nil
}

func sendOffline(c *conn.Conn, deposits []*util.DepositInfo, contractDetails *beaconDepositContract, fromAddress common.Address) {
	address := common.BytesToAddress(contractDetails.address)
	abi, err := abi.JSON(strings.NewReader(depositABI))
	cli.ErrCheck(err, quiet, "Failed to generate deposit contract ABI")

	for _, deposit := range deposits {
		var depositDataRoot [32]byte
		copy(depositDataRoot[:], deposit.DepositDataRoot)
		dataBytes, err := abi.Pack("deposit", deposit.PublicKey, deposit.WithdrawalCredentials, deposit.Signature, depositDataRoot)
		cli.ErrCheck(err, quiet, "Failed to create deposit transaction")
		var value *big.Int
		if beaconDepositForceZeroValue {
			value = big.NewInt(0)
		} else {
			if deposit.Amount == 0 {
				cli.Assert(viper.GetString("value") != "", quiet, "No value from either deposit data or command line; cannot create transaction")
				value, err = string2eth.StringToWei(viper.GetString("value"))
				cli.ErrCheck(err, quiet, "Failed to understand value")
			} else {
				value = new(big.Int).Mul(new(big.Int).SetUint64(deposit.Amount), big.NewInt(1000000000))
			}
		}
		var gasLimit uint64
		if beaconDepositOverrideGas != 0 {
			// Gas limit has been overridden.
			gasLimit = beaconDepositOverrideGas
		} else {
			// Need to set gas limit because it moves around a fair bit with the merkle tree calculations.
			// This is just above the maximum gas possible used by the contract, as calculated in section 4.2 of
			// https://raw.githubusercontent.com/runtimeverification/deposit-contract-verification/master/deposit-contract-verification.pdf
			gasLimit = 160000
		}
		signedTx, err := c.CreateSignedTransaction(context.Background(),
			&conn.TransactionData{
				From:     fromAddress,
				To:       &address,
				Value:    value,
				GasLimit: &gasLimit,
				Data:     dataBytes,
			})
		cli.ErrCheck(err, quiet, "Failed to create signed transaction")
		buf := new(bytes.Buffer)
		err = signedTx.EncodeRLP(buf)
		cli.ErrCheck(err, quiet, "Failed to encode signed transaction")
		fmt.Printf("%#x\n", buf.Bytes())
	}
}

func sendOnline(deposits []*util.DepositInfo, contractDetails *beaconDepositContract, fromAddress common.Address) {
	address := common.BytesToAddress(contractDetails.address)

	contract, err := contracts.NewEth2Deposit(address, c.Client())
	cli.ErrCheck(err, quiet, "Failed to obtain deposit contract")

	cli.Assert(len(deposits) > 0, quiet, "No deposit data supplied")

	for _, deposit := range deposits {
		opts, err := generateTxOpts(fromAddress)
		cli.ErrCheck(err, quiet, "Failed to generate deposit options")
		if beaconDepositForceZeroValue {
			opts.Value = big.NewInt(0)
		} else {
			// Need to override the value with the info from the JSON (if present).
			if deposit.Amount == 0 {
				cli.Assert(opts.Value.Cmp(big.NewInt(0)) != 0, quiet, "No value from either deposit data or command line; cannot create transaction")
				opts.Value, err = string2eth.StringToWei(viper.GetString("value"))
				cli.ErrCheck(err, quiet, "Failed to understand value")
			} else {
				opts.Value = new(big.Int).Mul(new(big.Int).SetUint64(deposit.Amount), big.NewInt(1000000000))
			}
		}

		if beaconDepositOverrideGas != 0 {
			// Gas limit has been overridden.
			opts.GasLimit = beaconDepositOverrideGas
		} else {
			// Need to set gas limit because it moves around a fair bit with the merkle tree calculations.
			// This is just above the maximum gas possible used by the contract, as calculated in section 4.2 of
			// https://raw.githubusercontent.com/runtimeverification/deposit-contract-verification/master/deposit-contract-verification.pdf
			opts.GasLimit = 160000
		}

		// Would be good to recalculate signature to ensure correcteness, but need a pure Go BLS implementation.

		// Check thegraph to see if there is already a deposit for this validator public key.
		if contractDetails.subgraph != "" {
			cli.ErrCheck(graphCheck(contractDetails.subgraph, deposit.PublicKey, opts.Value.Uint64(), deposit.WithdrawalCredentials), quiet, "Existing deposit check")
		}

		outputIf(verbose, fmt.Sprintf("Creating %s deposit for %s", string2eth.WeiToString(big.NewInt(int64(deposit.Amount)), true), deposit.Account))

		_, err = c.NextNonce(context.Background(), fromAddress)
		cli.ErrCheck(err, quiet, "Failed to obtain next nonce")
		var depositDataRoot [32]byte
		copy(depositDataRoot[:], deposit.DepositDataRoot)

		opts.GasFeeCap, opts.GasTipCap, err = c.CalculateFees()
		cli.ErrCheck(err, quiet, "Failed to obtain fees")

		signedTx, err := contract.Deposit(opts, deposit.PublicKey, deposit.WithdrawalCredentials, deposit.Signature, depositDataRoot)
		cli.ErrCheck(err, quiet, "Failed to send deposit")

		handleSubmittedTransaction(signedTx, log.Fields{
			"group":                        "beacon",
			"command":                      "deposit",
			"depositPublicKey":             fmt.Sprintf("%#x", deposit.PublicKey),
			"depositWithdrawalCredentials": fmt.Sprintf("%#x", deposit.WithdrawalCredentials),
			"depositSignature":             fmt.Sprintf("%#x", deposit.Signature),
			"depositDataRoot":              fmt.Sprintf("%#x", deposit.DepositDataRoot),
		}, false)
	}
}

// graphCheck checks against a subgraph to see if there is already a deposit for this validator key.
func graphCheck(subgraph string, validatorPubKey []byte, amount uint64, withdrawalCredentials []byte) error {
	query := fmt.Sprintf(`{"query": "{deposits(where: {validatorPubKey:\"%#x\"}) { id amount withdrawalCredentials }}"}`, validatorPubKey)
	url := fmt.Sprintf("https://api.thegraph.com/subgraphs/name/%s", subgraph)
	// #nosec G107
	graphResp, err := http.Post(url, "application/json", bytes.NewBufferString(query))
	if err != nil {
		return errors.Wrap(err, "failed to check if there is already a deposit for this validator")
	}
	defer graphResp.Body.Close()
	body, err := io.ReadAll(graphResp.Body)
	if err != nil {
		return errors.Wrap(err, "bad information returned from existing deposit check")
	}
	outputIf(debug, fmt.Sprintf("Received response from subgraph: %s", string(body)))

	type graphDeposit struct {
		Index                 string `json:"index"`
		Amount                string `json:"amount"`
		WithdrawalCredentials string `json:"withdrawalCredentials"`
	}
	type graphData struct {
		Deposits []*graphDeposit `json:"deposits,omitempty"`
	}
	type graphError struct {
		Message string `json:"message,omitempty"`
	}
	type graphResponse struct {
		Data   *graphData    `json:"data,omitempty"`
		Errors []*graphError `json:"errors,omitempty"`
	}

	var response graphResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return errors.Wrap(err, "invalid data returned from existing deposit check")
	}

	if response.Errors != nil {
		return errors.Wrap(err, fmt.Sprintf("error from graph server: %s", response.Errors[0].Message))
	}
	if response.Data != nil && len(response.Data.Deposits) > 0 {
		totalDeposited := int64(0)
		for _, deposit := range response.Data.Deposits {
			depositAmount, err := strconv.ParseUint(deposit.Amount, 10, 64)
			if err != nil {
				return errors.Wrap(err, fmt.Sprintf("invalid deposit amount from pre-existing deposit %s", deposit.Amount))
			}
			totalDeposited += int64(depositAmount)
		}
		if totalDeposited >= 32000000000 {
			if !beaconDepositAllowDuplicateDeposit {
				depositedWei := new(big.Int).Mul(big.NewInt(totalDeposited), big.NewInt(1000000000))
				return fmt.Errorf("there has already been %s deposited to this validator.  If you really want to add more funds to this validator use the --allow-duplicate-deposit option", string2eth.WeiToString(depositedWei, true))
			}
		}
		if totalDeposited+int64(amount) > 32000000000 {
			if !(beaconDepositAllowDuplicateDeposit || beaconDepositAllowExcessiveDeposit) {
				totalWei := new(big.Int).Mul(big.NewInt(totalDeposited+int64(amount)), big.NewInt(1000000000))
				return fmt.Errorf("this deposit will increase the validator's total deposits to %s.   If you really want to add these funds to this validator use the --allow-duplicate-deposit and --allow-excessive-deposit options", string2eth.WeiToString(totalWei, true))
			}
		}
	}
	return nil
}

func fetchBeaconDepositContract(contractAddress string, network string) (*beaconDepositContract, error) {
	var address []byte
	var err error
	if contractAddress != "" {
		address, err = hex.DecodeString(strings.TrimPrefix(contractAddress, "0x"))
		if err != nil {
			return nil, errors.Wrap(err, "invalid contract address")
		}
	}

	if len(address) > 0 {
		for _, contract := range beaconDepositKnownContracts {
			if bytes.Equal(address, contract.address) && c.ChainID().Cmp(contract.chainID) == 0 {
				return contract, nil
			}
		}

		// An address has been given but we don't recognise it.
		if beaconDepositAllowUnknownContract {
			// We allow this; return a synthetic contract definition.
			return &beaconDepositContract{
				network:    "user-supplied network",
				chainID:    c.ChainID(),
				address:    address,
				minVersion: 0,
				maxVersion: 999,
			}, nil
		}
		return nil, errors.New(`address does not match a known contract.

If you are sure you want to send to this address you can add --allow-unknown-contract to force this.  You will also need to supply a value for --network to ensure the deposit is sent on your desired network`)
	}

	if network != "" {
		for _, contract := range beaconDepositKnownContracts {
			if strings.EqualFold(network, contract.network) {
				return contract, nil
			}
		}
		return nil, errors.New("unknown Ethereum 2 network")
	}

	return nil, errors.New("not found")
}

func init() {
	beaconCmd.AddCommand(beaconDepositCmd)
	beaconFlags(beaconDepositCmd)
	beaconDepositCmd.Flags().StringVar(&beaconDepositData, "data", "", "The data for the deposit, provided by ethdo or a similar command")
	beaconDepositCmd.Flags().StringVar(&beaconDepositFrom, "from", "", "The account from which to send the deposit")
	beaconDepositCmd.Flags().BoolVar(&beaconDepositAllowUnknownContract, "allow-unknown-contract", false, "Allow sending to a contract address that is unknown by Ethereal (WARNING: only if you know what you are doing)")
	beaconDepositCmd.Flags().BoolVar(&beaconDepositAllowOldData, "allow-old-data", false, "Allow sending from an older version of deposit data than supported (WARNING: only if you know what you are doing)")
	beaconDepositCmd.Flags().BoolVar(&beaconDepositAllowNewData, "allow-new-data", false, "Allow sending from a newer version of deposit data than supported (WARNING: only if you know what you are doing)")
	beaconDepositCmd.Flags().BoolVar(&beaconDepositAllowExcessiveDeposit, "allow-excessive-deposit", false, "Allow sending more than 32 Ether in a single deposit (WARNING: only if you know what you are doing)")
	beaconDepositCmd.Flags().BoolVar(&beaconDepositAllowDuplicateDeposit, "allow-duplicate-deposit", false, "Allow sending multiple deposits with the same validator public key (WARNING: only if you know what you are doing)")
	beaconDepositCmd.Flags().BoolVar(&beaconDepositForceZeroValue, "force-zero-value", false, "Sending the deposit with 0 Ether regardless of the information in the deposit data")
	beaconDepositCmd.Flags().StringVar(&beaconDepositContractAddress, "address", "", "The contract address to which to send the deposit (overrides the value obtained from eth2network)")
	beaconDepositCmd.Flags().StringVar(&beaconDepositEth2Network, "eth2network", "mainnet", "The name of the Ethereum 2 network for which to send the deposit (mainnet/prater/ropsten/sepolia)")
	beaconDepositCmd.Flags().Uint64Var(&beaconDepositOverrideGas, "override-gas", 0, "Override the gas limit for the deposit transaction")
	addTransactionFlags(beaconDepositCmd, "the account from which to send the deposit")
}
