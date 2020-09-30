// Copyright © 2017-2020 Weald Technology Trading
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
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/wealdtech/ethereal/cli"
	"github.com/wealdtech/ethereal/util"
	"github.com/wealdtech/ethereal/util/contracts"
	ens "github.com/wealdtech/go-ens/v3"
	string2eth "github.com/wealdtech/go-string2eth"
)

// depositABI contains the ABI for the deposit contract.
var depositABI = `[{"name":"deposit","outputs":[],"inputs":[{"type":"bytes","name":"pubkey"},{"type":"bytes","name":"withdrawal_credentials"},{"type":"bytes","name":"signature"},{"type":"bytes32","name":"deposit_data_root"}],"constant":false,"payable":true,"type":"function"}]`

var beaconDepositData string
var beaconDepositFrom string
var beaconDepositAllowOldData bool
var beaconDepositAllowNewData bool
var beaconDepositAllowExcessiveDeposit bool
var beaconDepositAllowUnknownContract bool
var beaconDepositAllowDuplicateDeposit bool
var beaconDepositContractAddress string
var beaconDepositEth2Network string

type ethdoDepositData struct {
	Account               string `json:"account"`
	PublicKey             string `json:"pubkey"`
	WithdrawalCredentials string `json:"withdrawal_credentials"`
	Signature             string `json:"signature"`
	DepositDataRoot       string `json:"deposit_data_root"`
	Value                 uint64 `json:"value"`
	Version               uint64 `json:"version"`
}

type beaconDepositContract struct {
	network    string
	chainID    *big.Int
	address    []byte
	minVersion uint64
	maxVersion uint64
	subgraph   string
}

var beaconDepositKnownContracts = []*beaconDepositContract{
	{
		network:    "Topaz",
		chainID:    big.NewInt(5),
		address:    util.MustDecodeHexString("0x5ca1e00004366ac85f492887aaab12d0e6418876"),
		minVersion: 1,
		maxVersion: 1,
		subgraph:   "attestantio/eth2deposits-topaz",
	},
	{
		network:    "Onyx",
		chainID:    big.NewInt(5),
		address:    util.MustDecodeHexString("0x0f0f0fc0530007361933eab5db97d09acdd6c1c8"),
		minVersion: 2,
		maxVersion: 2,
		subgraph:   "attestantio/eth2deposits-onyx",
	},
	{
		network:    "Altona",
		chainID:    big.NewInt(5),
		address:    util.MustDecodeHexString("0x16e82D77882A663454Ef92806b7DeCa1D394810f"),
		minVersion: 2,
		maxVersion: 2,
		subgraph:   "attestantio/eth2deposits-altona",
	},
	{
		network:    "Medalla",
		chainID:    big.NewInt(5),
		address:    util.MustDecodeHexString("0x07b39F4fDE4A38bACe212b546dAc87C58DfE3fDC"),
		minVersion: 2,
		maxVersion: 3,
		subgraph:   "attestantio/eth2deposits-medalla",
	},
	{
		network:    "Spadina",
		chainID:    big.NewInt(5),
		address:    util.MustDecodeHexString("0x48B597F4b53C21B48AD95c7256B49D1779Bd5890"),
		minVersion: 2,
		maxVersion: 3,
		subgraph:   "attestantio/eth2deposits-spadina",
	},
	{
		network:    "Zinken",
		chainID:    big.NewInt(5),
		address:    util.MustDecodeHexString("0x99F0Ec06548b086E46Cb0019C78D0b9b9F36cD53"),
		minVersion: 2,
		maxVersion: 3,
		subgraph:   "attestantio/eth2deposits-zinken",
	},
}

// beaconDepositCmd represents the beacon deposit command
var beaconDepositCmd = &cobra.Command{
	Use:   "deposit",
	Short: "Deposit Ether to the beacon contract.",
	Long: `Deposit Ether to the Ethereum 2 beacon contract, either creating or supplementing a validator.  For example:

    ethereal beacon deposit --data=/home/me/depositdata.json --from=0x.... --passphrase="my secret passphrase"

Note that at current this deposits Ether to the Prysm test deposit contract on Goerli.  Other networks and deposit contracts are not supported.

The depositdata.json file can be generated by ethdo.  The data can be an array of deposits, in which case they will be processed sequentially.

The keystore for the account that owns the name must be local (i.e. listed with 'get accounts list') and unlockable with the supplied passphrase.

This will return an exit status of 0 if the transaction is successfully submitted (and mined if --wait is supplied), 1 if the transaction is not successfully submitted, and 2 if the transaction is successfully submitted but not mined within the supplied time limit.`,
	Run: func(cmd *cobra.Command, args []string) {
		cli.Assert(chainID.Cmp(big.NewInt(5)) == 0, quiet, "This command is only supported on the Goerli network")

		cli.Assert(beaconDepositData != "", quiet, "--data is required")
		depositInfo, err := loadDepositInfo(beaconDepositData)

		// Fetch the contract details.
		cli.Assert(beaconDepositContractAddress != "" || beaconDepositEth2Network != "", quiet, "one of --address or --eth2network is required")
		contract, err := fetchBeaconDepositContract(beaconDepositContractAddress, beaconDepositEth2Network)
		cli.ErrCheck(err, quiet, `Deposit contract is unknown.  This means you are either running an old version of ethereal, or are attempting to send to the wrong network or a custom contract.  You should confirm that you are on the latest version of Ethereal by comparing the output of running "ethereal version" with the release information at https://github.com/wealdtech/ethereal/releases and upgrading where appropriate.

If you are *completely sure* you know what you are doing, you can use the --allow-unknown-contract option to carry out this transaction.  Otherwise, please seek support to ensure you do not lose your Ether.`)
		outputIf(verbose && contractName != "", fmt.Sprintf("Deposit contract is %s", contract.network))

		// Confirm the deposit data before sending any.
		for i := range depositInfo {
			cli.Assert(len(depositInfo[i].PublicKey) > 0, quiet, fmt.Sprintf("No public key for deposit %d", i))
			cli.Assert(len(depositInfo[i].DepositDataRoot) > 0, quiet, fmt.Sprintf("No data root for deposit %d", i))
			cli.Assert(len(depositInfo[i].Signature) > 0, quiet, fmt.Sprintf("No signature for deposit %d", i))
			cli.Assert(len(depositInfo[i].WithdrawalCredentials) > 0, quiet, fmt.Sprintf("No withdrawal credentials for deposit %d", i))
			cli.Assert(depositInfo[i].Amount >= 1000000000, quiet, fmt.Sprintf("Deposit too small for deposit %d", i))
			cli.Assert(depositInfo[i].Amount <= 32000000000 || beaconDepositAllowExcessiveDeposit, quiet, fmt.Sprintf(`Deposit more than 32 Ether for deposit %d.  Any amount above 32 Ether that is deposited will not count towards the validator's effective balance, and is effectively wasted.

If you really want to do this use the --allow-excessive-deposit option.`, i))

			cli.Assert(beaconDepositAllowOldData || depositInfo[i].Version >= contract.minVersion, quiet, `Data generated by ethdo is old and possibly inaccurate.  This means you need to upgrade your version of ethdo (or you are sending your deposit to the wrong contract or network); please do so by visiting https://github.com/wealdtech/ethdo and following the installation instructions there.  Once you have done this please regenerate your deposit data and try again.

If you are *completely sure* you know what you are doing, you can use the --allow-old-data option to carry out this transaction.  Otherwise, please seek support to ensure you do not lose your Ether.`)
			cli.Assert(beaconDepositAllowNewData || depositInfo[i].Version <= contract.maxVersion, quiet, `Data generated by ethdo is newer than supported.  This means you need to upgrade your version of ethereal (or you are sending your deposit to the wrong contract or network); please do so by visiting https://github.com/wealdtech/ethereal and following the installation instructions there.  Once you have done this please try again.

If you are *completely sure* you know what you are doing, you can use the --allow-new-data option to carry out this transaction.  Otherwise, please seek support to ensure you do not lose your Ether.`)
		}

		cli.Assert(beaconDepositFrom != "", quiet, "--from is required")
		fromAddress, err := ens.Resolve(client, beaconDepositFrom)
		cli.ErrCheck(err, quiet, "Failed to obtain address for --from")

		if offline {
			sendOffline(depositInfo, contract, fromAddress)
		} else {
			sendOnline(depositInfo, contract, fromAddress)
		}
		os.Exit(_exit_success)
	},
}

func loadDepositInfo(input string) ([]*util.DepositInfo, error) {
	var err error
	var data []byte
	// Input could be JSON or a path to JSON
	if strings.HasPrefix(input, "{") {
		// Looks like JSON
		data = []byte("[" + input + "]")
	} else if strings.HasPrefix(input, "[") {
		// Looks like JSON array
		data = []byte(input)
	} else {
		// Assume it's a path to JSON
		data, err = ioutil.ReadFile(input)
		if err != nil {
			return nil, errors.Wrap(err, "failed to find deposit data file")
		}
		if data[0] == '{' {
			data = []byte("[" + string(data) + "]")
		}
	}

	depositInfo, err := util.DepositInfoFromJSON(data)
	if err != nil {
		return nil, errors.New("failed to obtain deposit information")
	}
	if len(depositInfo) == 0 {
		return nil, errors.New("no deposit information supplied")
	}
	return depositInfo, nil
}

func sendOffline(deposits []*util.DepositInfo, contractDetails *beaconDepositContract, fromAddress common.Address) {
	address := common.BytesToAddress(contractDetails.address)
	abi, err := abi.JSON(strings.NewReader(depositABI))
	cli.ErrCheck(err, quiet, "Failed to generate deposit contract ABI")

	for _, deposit := range deposits {
		dataBytes, err := abi.Pack("deposit", deposit.PublicKey, deposit.WithdrawalCredentials, deposit.Signature, deposit.DepositDataRoot)
		cli.ErrCheck(err, quiet, "Failed to create deposit transaction")
		value := new(big.Int).Mul(new(big.Int).SetUint64(deposit.Amount), big.NewInt(1000000000))
		signedTx, err := createSignedTransaction(fromAddress, &address, value, 500000, dataBytes)
		cli.ErrCheck(err, quiet, "Failed to create signed transaction")
		buf := new(bytes.Buffer)
		err = signedTx.EncodeRLP(buf)
		cli.ErrCheck(err, quiet, "Failed to encode signed transaction")
		fmt.Printf("%#x\n", buf.Bytes())
	}
}

func sendOnline(deposits []*util.DepositInfo, contractDetails *beaconDepositContract, fromAddress common.Address) {
	address := common.BytesToAddress(contractDetails.address)

	contract, err := contracts.NewEth2Deposit(address, client)
	cli.ErrCheck(err, quiet, "Failed to obtain deposit contract")

	cli.Assert(len(deposits) > 0, quiet, "No deposit data supplied")

	for _, deposit := range deposits {
		opts, err := generateTxOpts(fromAddress)
		cli.ErrCheck(err, quiet, "Failed to generate deposit options")
		// Need to override the value with the info from the JSON
		opts.Value = new(big.Int).Mul(new(big.Int).SetUint64(deposit.Amount), big.NewInt(1000000000))

		// Need to set gas limit because it moves around a fair bit with the merkle tree calculations.
		// This is just above the maximum gas possible used by the contract, as calculated in
		// https://raw.githubusercontent.com/runtimeverification/deposit-contract-verification/master/deposit-contract-verification.pdf
		opts.GasLimit = 160000

		// TODO recalculate signature to ensure correcteness (needs a pure Go BLS implementation).

		// Check thegraph to see if there is already a deposit for this validator public key.
		if contractDetails.subgraph != "" {
			cli.ErrCheck(graphCheck(contractDetails.subgraph, deposit.PublicKey, opts.Value.Uint64(), deposit.WithdrawalCredentials), quiet, "Existing deposit check")
		}

		outputIf(verbose, fmt.Sprintf("Creating %s deposit for %s", string2eth.WeiToString(big.NewInt(int64(deposit.Amount)), true), deposit.Account))

		_, err = nextNonce(fromAddress)
		cli.ErrCheck(err, quiet, "Failed to obtain next nonce")
		var depositDataRoot [32]byte
		copy(depositDataRoot[:], deposit.DepositDataRoot)
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
	graphResp, err := http.Post(url, "application/json", bytes.NewBufferString(query))
	if err != nil {
		return errors.Wrap(err, "failed to check if there is already a deposit for this validator")
	}
	defer graphResp.Body.Close()
	body, err := ioutil.ReadAll(graphResp.Body)
	if err != nil {
		return errors.Wrap(err, "bad information returned from existing deposit check")
	}

	type graphDeposit struct {
		Index                 string `json:"index"`
		Amount                string `json:"amount"`
		WithdrawalCredentials string `json:"withdrawalCredentials"`
	}
	type graphData struct {
		Deposits []*graphDeposit `json:"deposits,omitempty"`
	}
	type graphResponse struct {
		Data *graphData `json:"data,omitempty"`
	}

	var response graphResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return errors.Wrap(err, "invalid data returned from existing deposit check")
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
			if bytes.Equal(address, contract.address) && chainID.Cmp(contract.chainID) == 0 {
				return contract, nil
			}
		}

		// An address has been given but we don't recognise it.
		if beaconDepositAllowUnknownContract {
			// We allow this; return a synthetic contract definition.
			return &beaconDepositContract{
				network:    "user-supplied network",
				chainID:    chainID,
				address:    address,
				minVersion: 0,
				maxVersion: 999,
			}, nil
		}
		return nil, errors.New(`address does not match a known contract.

If you are sure you want to send to this address you can add --allow-unknown-contract to force this.  You will also need to supply a value for --network to ensure the deposit is sent on your desired network.`)
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
	beaconDepositCmd.Flags().StringVar(&beaconDepositContractAddress, "address", "", "The address to which to send the deposit (overrides network)")
	beaconDepositCmd.Flags().StringVar(&beaconDepositEth2Network, "eth2network", "medalla", "The name of the network to send the deposit to (topaz/onyx/altona/medalla/spadina/zinken)")
	addTransactionFlags(beaconDepositCmd, "passphrase for the account that owns the account")
}
