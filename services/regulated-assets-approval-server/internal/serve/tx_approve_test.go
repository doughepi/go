package serve

import (
	"context"
	"net/http"
	"testing"

	"github.com/stellar/go/amount"
	"github.com/stellar/go/clients/horizonclient"
	"github.com/stellar/go/keypair"
	"github.com/stellar/go/network"
	"github.com/stellar/go/protocols/horizon"
	"github.com/stellar/go/protocols/horizon/base"
	"github.com/stellar/go/services/regulated-assets-approval-server/internal/db/dbtest"
	"github.com/stellar/go/txnbuild"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTxApproveHandlerValidate(t *testing.T) {
	// empty asset issuer KP.
	h := txApproveHandler{}
	err := h.validate()
	require.EqualError(t, err, "issuer keypair cannot be nil")

	// empty asset code.
	issuerAccKeyPair := keypair.MustRandom()
	h = txApproveHandler{
		issuerKP: issuerAccKeyPair,
	}
	err = h.validate()
	require.EqualError(t, err, "asset code cannot be empty")

	// No Horizon client.
	h = txApproveHandler{
		issuerKP:  issuerAccKeyPair,
		assetCode: "FOOBAR",
	}
	err = h.validate()
	require.EqualError(t, err, "horizon client cannot be nil")

	// No network passphrase.
	horizonMock := horizonclient.MockClient{}
	h = txApproveHandler{
		issuerKP:      issuerAccKeyPair,
		assetCode:     "FOOBAR",
		horizonClient: &horizonMock,
	}
	err = h.validate()
	require.EqualError(t, err, "network passphrase cannot be empty")

	// No db.
	h = txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         "FOOBAR",
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
	}
	err = h.validate()
	require.EqualError(t, err, "database cannot be nil")

	// Empty kycThreshold.
	db := dbtest.Open(t)
	defer db.Close()
	conn := db.Open()
	defer conn.Close()
	h = txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         "FOOBAR",
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
		db:                conn,
	}
	err = h.validate()
	require.EqualError(t, err, "kyc threshold cannot be less than or equal to zero")

	// Negative kycThreshold.
	h = txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         "FOOBAR",
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
		db:                conn,
		kycThreshold:      -1,
	}
	err = h.validate()
	require.EqualError(t, err, "kyc threshold cannot be less than or equal to zero")

	// no baseURL.
	h = txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         "FOOBAR",
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
		db:                conn,
		kycThreshold:      1,
	}
	err = h.validate()
	require.EqualError(t, err, "base url cannot be empty")

	// Success.
	h = txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         "FOOBAR",
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
		db:                conn,
		kycThreshold:      1,
		baseURL:           "https://sep8-server.test",
	}
	err = h.validate()
	require.NoError(t, err)
}

func TestTxApproveHandlerCheckSequenceNum(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	defer db.Close()
	conn := db.Open()
	defer conn.Close()

	// Create txApproveHandler.
	issuerAccKeyPair := keypair.MustRandom()
	senderAccKP := keypair.MustRandom()
	receiverAccKP := keypair.MustRandom()
	horizonMock := horizonclient.MockClient{}
	horizonMock.
		On("AccountDetail", horizonclient.AccountRequest{AccountID: senderAccKP.Address()}).
		Return(horizon.Account{
			AccountID: senderAccKP.Address(),
			Sequence:  "5",
		}, nil)

	kycThresholdAmount, err := amount.ParseInt64("500")
	require.NoError(t, err)
	assetGOAT := txnbuild.CreditAsset{
		Code:   "GOAT",
		Issuer: issuerAccKeyPair.Address(),
	}
	h := txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         assetGOAT.GetCode(),
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
		db:                conn,
		kycThreshold:      kycThresholdAmount,
		baseURL:           "https://sep8-server.test",
	}

	// Prepare transaction with correct sequence number.
	acc, err := h.horizonClient.AccountDetail(horizonclient.AccountRequest{AccountID: senderAccKP.Address()})
	require.NoError(t, err)
	tx, err := txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount: &horizon.Account{
				AccountID: senderAccKP.Address(),
				Sequence:  "5",
			},
			IncrementSequenceNum: true,
			Operations: []txnbuild.Operation{
				&txnbuild.Payment{
					SourceAccount: senderAccKP.Address(),
					Destination:   receiverAccKP.Address(),
					Amount:        "1",
					Asset:         assetGOAT,
				},
			},
			BaseFee:    txnbuild.MinBaseFee,
			Timebounds: txnbuild.NewInfiniteTimeout(),
		},
	)
	require.NoError(t, err)

	// TEST nil response if transaction source account seq num is equal to account sequence+1.
	rejectedResponse, err := h.checkSequenceNum(ctx, tx, acc)
	require.NoError(t, err)
	assert.Nil(t, rejectedResponse)

	// Prepare transaction where sourceAccount seq num in transaction is too far in the future.
	tx, err = txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount: &horizon.Account{
				AccountID: senderAccKP.Address(),
				Sequence:  "50",
			},
			IncrementSequenceNum: true,
			Operations: []txnbuild.Operation{
				&txnbuild.Payment{
					SourceAccount: senderAccKP.Address(),
					Destination:   receiverAccKP.Address(),
					Amount:        "1",
					Asset:         assetGOAT,
				},
			},
			BaseFee:    txnbuild.MinBaseFee,
			Timebounds: txnbuild.NewInfiniteTimeout(),
		},
	)
	require.NoError(t, err)

	// TEST "rejected" response if transaction source account seq num is not equal to account sequence+1.
	rejectedResponse, err = h.checkSequenceNum(ctx, tx, acc)
	require.NoError(t, err)
	wantRejectedResponse := txApprovalResponse{
		Status:     "rejected",
		Error:      "Invalid transaction sequence number.",
		StatusCode: http.StatusBadRequest,
	}
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)

	// TEST error if transaction source account seq num is malformed; set sequence to something not parsable.
	acc.Sequence = "TEN"
	rejectedResponse, err = h.checkSequenceNum(ctx, tx, acc)
	assert.Nil(t, rejectedResponse)
	assert.EqualError(t, err, "parsing account sequence number \"TEN\" from string to int64: strconv.ParseInt: parsing \"TEN\": invalid syntax")
}

func TestTxApproveHandler_validateInput(t *testing.T) {
	h := txApproveHandler{}
	ctx := context.Background()

	// empty tx
	in := txApproveRequest{}
	txApprovalResp, gotTx := h.validateInput(ctx, in)
	require.Equal(t, NewRejectedTxApprovalResponse("Missing parameter \"tx\"."), txApprovalResp)
	require.Nil(t, gotTx)

	// invalid tx
	in = txApproveRequest{Tx: "foobar"}
	txApprovalResp, gotTx = h.validateInput(ctx, in)
	require.Equal(t, NewRejectedTxApprovalResponse("Invalid parameter \"tx\"."), txApprovalResp)
	require.Nil(t, gotTx)

	// invaid for fee bump transaction
	in = txApproveRequest{Tx: "AAAABQAAAAAo/cVyQxyGh7F/Vsj0BzfDYuOJvrwgfHGyqYFpHB5RCAAAAAAAAADIAAAAAgAAAAAo/cVyQxyGh7F/Vsj0BzfDYuOJvrwgfHGyqYFpHB5RCAAAAGQAEfDJAAAAAQAAAAEAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAEAAAAAAAAAAQAAAAAo/cVyQxyGh7F/Vsj0BzfDYuOJvrwgfHGyqYFpHB5RCAAAAAAAAAAAAJiWgAAAAAAAAAAAAAAAAAAAAAA="}
	txApprovalResp, gotTx = h.validateInput(ctx, in)
	require.Equal(t, NewRejectedTxApprovalResponse("Invalid parameter \"tx\"."), txApprovalResp)
	require.Nil(t, gotTx)

	// forbids setting issuer as tx.SourceAccount
	clientKP := keypair.MustRandom()
	h.issuerKP = keypair.MustRandom()

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount: &horizon.Account{
			AccountID: h.issuerKP.Address(),
			Sequence:  "1",
		},
		IncrementSequenceNum: true,
		Timebounds:           txnbuild.NewInfiniteTimeout(),
		BaseFee:              300,
		Operations: []txnbuild.Operation{
			&txnbuild.Payment{
				Destination: clientKP.Address(),
				Amount:      "1",
				Asset:       txnbuild.NativeAsset{},
			},
		},
	})
	require.NoError(t, err)
	txe, err := tx.Base64()
	require.NoError(t, err)

	in.Tx = txe
	txApprovalResp, gotTx = h.validateInput(ctx, in)
	require.Equal(t, NewRejectedTxApprovalResponse("Transaction source account is invalid."), txApprovalResp)
	require.Nil(t, gotTx)

	// forbids setting issuer as op.SourceAccount if op is not AllowTrust
	tx, err = txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount: &horizon.Account{
			AccountID: clientKP.Address(),
			Sequence:  "1",
		},
		IncrementSequenceNum: true,
		Timebounds:           txnbuild.NewInfiniteTimeout(),
		BaseFee:              300,
		Operations: []txnbuild.Operation{
			&txnbuild.Payment{
				Destination:   clientKP.Address(),
				Amount:        "1",
				Asset:         txnbuild.NativeAsset{},
				SourceAccount: h.issuerKP.Address(),
			},
		},
	})
	require.NoError(t, err)
	txe, err = tx.Base64()
	require.NoError(t, err)

	in.Tx = txe
	txApprovalResp, gotTx = h.validateInput(ctx, in)
	require.Equal(t, NewRejectedTxApprovalResponse("There is one or more unauthorized operations in the provided transaction."), txApprovalResp)
	require.Nil(t, gotTx)

	// success
	tx, err = txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount: &horizon.Account{
			AccountID: clientKP.Address(),
			Sequence:  "1",
		},
		IncrementSequenceNum: true,
		Timebounds:           txnbuild.NewInfiniteTimeout(),
		BaseFee:              300,
		Operations: []txnbuild.Operation{
			&txnbuild.Payment{
				Destination: clientKP.Address(),
				Amount:      "1.0000000",
				Asset:       txnbuild.NativeAsset{},
			},
		},
	})
	require.NoError(t, err)
	txe, err = tx.Base64()
	require.NoError(t, err)

	in.Tx = txe
	txApprovalResp, gotTx = h.validateInput(ctx, in)
	require.Nil(t, txApprovalResp)
	require.Equal(t, gotTx, tx)
}

func TestTxApproveHandlerTxApprove(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	defer db.Close()
	conn := db.Open()
	defer conn.Close()

	// Perpare accounts on mock horizon.
	issuerAccKeyPair := keypair.MustRandom()
	senderAccKP := keypair.MustRandom()
	receiverAccKP := keypair.MustRandom()
	assetGOAT := txnbuild.CreditAsset{
		Code:   "GOAT",
		Issuer: issuerAccKeyPair.Address(),
	}
	horizonMock := horizonclient.MockClient{}
	horizonMock.
		On("AccountDetail", horizonclient.AccountRequest{AccountID: issuerAccKeyPair.Address()}).
		Return(horizon.Account{
			AccountID: issuerAccKeyPair.Address(),
			Sequence:  "1",
			Balances: []horizon.Balance{
				{
					Asset:   base.Asset{Code: "ASSET", Issuer: issuerAccKeyPair.Address()},
					Balance: "0",
				},
			},
		}, nil)
	horizonMock.
		On("AccountDetail", horizonclient.AccountRequest{AccountID: senderAccKP.Address()}).
		Return(horizon.Account{
			AccountID: senderAccKP.Address(),
			Sequence:  "2",
		}, nil)
	horizonMock.
		On("AccountDetail", horizonclient.AccountRequest{AccountID: receiverAccKP.Address()}).
		Return(horizon.Account{
			AccountID: receiverAccKP.Address(),
			Sequence:  "3",
		}, nil)

	// Create tx-approve/ txApproveHandler.
	kycThresholdAmount, err := amount.ParseInt64("500")
	require.NoError(t, err)
	handler := txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         assetGOAT.GetCode(),
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
		db:                conn,
		kycThreshold:      kycThresholdAmount,
		baseURL:           "https://sep8-server.test",
	}

	// TEST "rejected" response if no transaction is submitted; with empty "tx" for txApprove.
	req := txApproveRequest{
		Tx: "",
	}
	rejectedResponse, err := handler.txApprove(ctx, req)
	require.NoError(t, err)
	wantRejectedResponse := txApprovalResponse{
		Status:     "rejected",
		Error:      `Missing parameter "tx".`,
		StatusCode: http.StatusBadRequest,
	}
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)

	// TEST "rejected" response if can't parse XDR; with malformed "tx" for txApprove.
	req = txApproveRequest{
		Tx: "BADXDRTRANSACTIONENVELOPE",
	}
	rejectedResponse, err = handler.txApprove(ctx, req)
	require.NoError(t, err)
	wantRejectedResponse = txApprovalResponse{
		Status:     "rejected",
		Error:      `Invalid parameter "tx".`,
		StatusCode: http.StatusBadRequest,
	}
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)

	// Prepare invalid(non generic transaction) "tx" for txApprove.
	senderAcc, err := handler.horizonClient.AccountDetail(horizonclient.AccountRequest{AccountID: senderAccKP.Address()})
	require.NoError(t, err)
	tx, err := txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount:        &senderAcc,
			IncrementSequenceNum: true,
			Operations: []txnbuild.Operation{
				&txnbuild.Payment{
					Destination: receiverAccKP.Address(),
					Amount:      "1",
					Asset:       assetGOAT,
				},
			},
			BaseFee:    txnbuild.MinBaseFee,
			Timebounds: txnbuild.NewInfiniteTimeout(),
		},
	)
	require.NoError(t, err)
	feeBumpTx, err := txnbuild.NewFeeBumpTransaction(
		txnbuild.FeeBumpTransactionParams{
			Inner:      tx,
			FeeAccount: receiverAccKP.Address(),
			BaseFee:    2 * txnbuild.MinBaseFee,
		},
	)
	require.NoError(t, err)
	feeBumpTxEnc, err := feeBumpTx.Base64()
	require.NoError(t, err)

	// TEST "rejected" response if a non generic transaction fails, same result as malformed XDR.
	req = txApproveRequest{
		Tx: feeBumpTxEnc,
	}
	rejectedResponse, err = handler.txApprove(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, &wantRejectedResponse, rejectedResponse) // wantRejectedResponse is identical to "if can't parse XDR".

	// Prepare transaction sourceAccount the same as the server issuer account for txApprove.
	issuerAcc, err := handler.horizonClient.AccountDetail(horizonclient.AccountRequest{AccountID: issuerAccKeyPair.Address()})
	require.NoError(t, err)
	tx, err = txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount:        &issuerAcc,
			IncrementSequenceNum: true,
			Operations: []txnbuild.Operation{
				&txnbuild.Payment{
					Destination: senderAccKP.Address(),
					Amount:      "1",
					Asset:       assetGOAT,
				},
			},
			BaseFee:    txnbuild.MinBaseFee,
			Timebounds: txnbuild.NewInfiniteTimeout(),
		},
	)
	require.NoError(t, err)
	txEnc, err := tx.Base64()
	require.NoError(t, err)

	// TEST "rejected" response for sender account; transaction sourceAccount the same as the server issuer account.
	req = txApproveRequest{
		Tx: txEnc,
	}
	rejectedResponse, err = handler.txApprove(ctx, req)
	require.NoError(t, err)
	wantRejectedResponse = txApprovalResponse{
		Status:     "rejected",
		Error:      "Transaction source account is invalid.",
		StatusCode: http.StatusBadRequest,
	}
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)

	// Prepare transaction where transaction's payment operation sourceAccount the same as the server issuer account.
	tx, err = txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount:        &senderAcc,
			IncrementSequenceNum: true,
			Operations: []txnbuild.Operation{
				&txnbuild.Payment{
					SourceAccount: issuerAccKeyPair.Address(),
					Destination:   senderAccKP.Address(),
					Amount:        "1",
					Asset:         assetGOAT,
				},
			},
			BaseFee:    txnbuild.MinBaseFee,
			Timebounds: txnbuild.NewInfiniteTimeout(),
		},
	)
	require.NoError(t, err)
	txEnc, err = tx.Base64()
	require.NoError(t, err)

	// TEST "rejected" response for sender account; payment operation sourceAccount the same as the server issuer account.
	req = txApproveRequest{
		Tx: txEnc,
	}
	rejectedResponse, err = handler.txApprove(ctx, req)
	require.NoError(t, err)
	wantRejectedResponse = txApprovalResponse{
		Status:     "rejected",
		Error:      "There is one or more unauthorized operations in the provided transaction.",
		StatusCode: http.StatusBadRequest,
	}
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)

	// Prepare transaction where operation is not a payment (in this case allowing trust for receiverAccKP).
	tx, err = txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount:        &senderAcc,
			IncrementSequenceNum: true,
			Operations: []txnbuild.Operation{
				&txnbuild.AllowTrust{
					Trustor:   receiverAccKP.Address(),
					Type:      assetGOAT,
					Authorize: true,
				},
			},
			BaseFee:    txnbuild.MinBaseFee,
			Timebounds: txnbuild.NewInfiniteTimeout(),
		},
	)
	require.NoError(t, err)
	txEnc, err = tx.Base64()

	// TEST "rejected" response if operation is not a payment (in this case allowing trust for receiverAccKP).
	req = txApproveRequest{
		Tx: txEnc,
	}
	rejectedResponse, err = handler.txApprove(ctx, req)
	require.NoError(t, err)
	wantRejectedResponse = txApprovalResponse{
		Status:     "rejected",
		Error:      "There is one or more unauthorized operations in the provided transaction.",
		StatusCode: http.StatusBadRequest,
	}
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)

	// Prepare transaction with multiple operations.
	tx, err = txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount:        &senderAcc,
			IncrementSequenceNum: true,
			Operations: []txnbuild.Operation{
				&txnbuild.Payment{
					SourceAccount: senderAccKP.Address(),
					Destination:   receiverAccKP.Address(),
					Amount:        "1",
					Asset:         assetGOAT,
				},
				&txnbuild.Payment{
					SourceAccount: senderAccKP.Address(),
					Destination:   receiverAccKP.Address(),
					Amount:        "2",
					Asset:         assetGOAT,
				},
			},
			BaseFee:    txnbuild.MinBaseFee,
			Timebounds: txnbuild.NewInfiniteTimeout(),
		},
	)
	require.NoError(t, err)
	txEnc, err = tx.Base64()
	require.NoError(t, err)

	// TEST "rejected" response for sender account; transaction with multiple operations.
	req = txApproveRequest{
		Tx: txEnc,
	}
	rejectedResponse, err = handler.txApprove(ctx, req)
	require.NoError(t, err)
	wantRejectedResponse = txApprovalResponse{
		Status:     "rejected",
		Error:      "Please submit a transaction with exactly one operation of type payment.",
		StatusCode: http.StatusBadRequest,
	}
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)

	// Prepare transaction where sourceAccount seq num too far in the future.
	tx, err = txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount: &horizon.Account{
				AccountID: senderAccKP.Address(),
				Sequence:  "50",
			},
			IncrementSequenceNum: true,
			Operations: []txnbuild.Operation{
				&txnbuild.Payment{
					SourceAccount: senderAccKP.Address(),
					Destination:   receiverAccKP.Address(),
					Amount:        "1",
					Asset:         assetGOAT,
				},
			},
			BaseFee:    txnbuild.MinBaseFee,
			Timebounds: txnbuild.NewInfiniteTimeout(),
		},
	)
	require.NoError(t, err)
	txEnc, err = tx.Base64()
	require.NoError(t, err)

	// TEST "rejected" response if transaction source account seq num is not equal to account sequence+1.
	req = txApproveRequest{
		Tx: txEnc,
	}
	rejectedResponse, err = handler.txApprove(ctx, req)
	require.NoError(t, err)
	wantRejectedResponse = txApprovalResponse{
		Status:     "rejected",
		Error:      "Invalid transaction sequence number.",
		StatusCode: http.StatusBadRequest,
	}
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)
}

func TestTxApproveHandlerKYCRequiredMessageIfNeeded(t *testing.T) {
	db := dbtest.Open(t)
	defer db.Close()
	conn := db.Open()
	defer conn.Close()

	// Create tx-approve/ txApproveHandler.
	issuerAccKeyPair := keypair.MustRandom()
	horizonMock := horizonclient.MockClient{}
	kycThresholdAmount, err := amount.ParseInt64("500")
	require.NoError(t, err)
	assetGOAT := txnbuild.CreditAsset{
		Code:   "GOAT",
		Issuer: issuerAccKeyPair.Address(),
	}
	h := txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         assetGOAT.GetCode(),
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
		db:                conn,
		kycThreshold:      kycThresholdAmount,
		baseURL:           "https://sep8-server.test",
	}

	// TEST if txApproveHandler is valid.
	err = h.validate()
	require.NoError(t, err)

	// Preparing payment op for kycRequiredMessageIfNeeded; payment amount is below kycThreshold.
	destinationKP := keypair.MustRandom()
	paymentOP := txnbuild.Payment{
		Destination: destinationKP.Address(),
		Amount:      "100",
		Asset:       assetGOAT,
	}

	// TEST No KYC needed response. actionRequiredMessage should be "".
	actionRequiredMessage, err := h.kycRequiredMessageIfNeeded(&paymentOP)
	require.NoError(t, err)
	require.Empty(t, actionRequiredMessage)

	// Prepare payment op for kycRequiredMessageIfNeeded; payment amount is malformed.
	paymentOP = txnbuild.Payment{
		Destination: destinationKP.Address(),
		Amount:      "ten",
		Asset:       assetGOAT,
	}

	// TEST kycRequiredMessageIfNeeded returns error.
	_, err = h.kycRequiredMessageIfNeeded(&paymentOP)
	assert.Contains(t,
		err.Error(),
		`parsing account payment amount from string to Int64: invalid amount format: ten`,
	)

	// Preparing payment op for kycRequiredMessageIfNeeded; payment amount is above kycThreshold.
	paymentOP = txnbuild.Payment{
		Destination: destinationKP.Address(),
		Amount:      "501",
		Asset:       assetGOAT,
	}

	// TEST Successful KYC required response.
	// actionRequiredMessage should return "Payments exceeding [kycThreshold] [assetCode] requires KYC approval..." message.
	actionRequiredMessage, err = h.kycRequiredMessageIfNeeded(&paymentOP)
	require.NoError(t, err)
	assert.Equal(t, `Payments exceeding 500.00 GOAT requires KYC approval. Please provide an email address.`, actionRequiredMessage)
}

func TestTxApproveHandlerHandleKYCRequiredOperationIfNeeded(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	defer db.Close()
	conn := db.Open()
	defer conn.Close()

	// Create tx-approve/ txApproveHandler.
	issuerAccKeyPair := keypair.MustRandom()
	horizonMock := horizonclient.MockClient{}
	kycThresholdAmount, err := amount.ParseInt64("500")
	require.NoError(t, err)
	assetGOAT := txnbuild.CreditAsset{
		Code:   "GOAT",
		Issuer: issuerAccKeyPair.Address(),
	}
	h := txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         assetGOAT.GetCode(),
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
		db:                conn,
		kycThreshold:      kycThresholdAmount,
		baseURL:           "https://sep8-server.test",
	}

	// TEST if txApproveHandler is valid.
	err = h.validate()
	require.NoError(t, err)

	// Prepare payment op whose amount is greater than 500 GOATs.
	sourceKP := keypair.MustRandom()
	destinationKP := keypair.MustRandom()
	paymentOP := txnbuild.Payment{
		SourceAccount: sourceKP.Address(),
		Destination:   destinationKP.Address(),
		Amount:        "501",
		Asset:         assetGOAT,
	}

	// TEST successful "action_required" response.
	actionRequiredTxApprovalResponse, err := h.handleKYCRequiredOperationIfNeeded(ctx, sourceKP.Address(), &paymentOP)
	require.NoError(t, err)
	wantTXApprovalResponse := txApprovalResponse{
		Status:       sep8Status("action_required"),
		Message:      `Payments exceeding 500.00 GOAT requires KYC approval. Please provide an email address.`,
		StatusCode:   http.StatusOK,
		ActionURL:    actionRequiredTxApprovalResponse.ActionURL,
		ActionMethod: "POST",
		ActionFields: []string{"email_address"},
	}
	assert.Equal(t, &wantTXApprovalResponse, actionRequiredTxApprovalResponse)

	// TEST if the kyc attempt was logged in db's accounts_kyc_status table.
	const q = `
	SELECT stellar_address
	FROM accounts_kyc_status
	WHERE stellar_address = $1
	`
	var stellarAddress string
	err = h.db.QueryRowContext(ctx, q, sourceKP.Address()).Scan(&stellarAddress)
	require.NoError(t, err)
	assert.Equal(t, sourceKP.Address(), stellarAddress)
}

func TestTestTxApproveHandlerValidateIncomingOperationsForSuccess(t *testing.T) {
	db := dbtest.Open(t)
	defer db.Close()
	conn := db.Open()
	defer conn.Close()

	// Create tx-approve/ txApproveHandler.
	issuerAccKeyPair := keypair.MustRandom()
	senderAccKP := keypair.MustRandom()
	receiverAccKP := keypair.MustRandom()
	horizonMock := horizonclient.MockClient{}
	kycThresholdAmount, err := amount.ParseInt64("500")
	require.NoError(t, err)
	assetGOAT := txnbuild.CreditAsset{
		Code:   "GOAT",
		Issuer: "",
	}
	h := txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         assetGOAT.GetCode(),
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
		db:                conn,
		kycThreshold:      kycThresholdAmount,
		baseURL:           "https://sep8-server.test",
	}

	// Prepare incoming operations to check if they're compliant.
	paymentOp := txnbuild.Payment{
		Destination:   receiverAccKP.Address(),
		SourceAccount: senderAccKP.Address(),
		Amount:        "1",
		Asset:         assetGOAT,
	}
	incomingOperations := []txnbuild.Operation{
		&txnbuild.AllowTrust{
			Trustor:       senderAccKP.Address(),
			Type:          assetGOAT,
			Authorize:     true,
			SourceAccount: issuerAccKeyPair.Address(),
		},
		&txnbuild.AllowTrust{
			Trustor:       receiverAccKP.Address(),
			Type:          assetGOAT,
			Authorize:     true,
			SourceAccount: issuerAccKeyPair.Address(),
		},
		&paymentOp,
		&txnbuild.AllowTrust{
			Trustor:       receiverAccKP.Address(),
			Type:          assetGOAT,
			Authorize:     false,
			SourceAccount: issuerAccKeyPair.Address(),
		},
		&txnbuild.AllowTrust{
			Trustor:       senderAccKP.Address(),
			Type:          assetGOAT,
			Authorize:     false,
			SourceAccount: issuerAccKeyPair.Address(),
		},
	}

	//TEST if incoming array of ops is compliant.
	ok := h.validateIncomingOperationsForSuccess(incomingOperations, senderAccKP.Address(), &paymentOp)
	assert.True(t, ok)

	//TEST if incoming array of ops is not in correct order.
	incomingOperations[0], incomingOperations[1] = incomingOperations[1], incomingOperations[0]
	ok = h.validateIncomingOperationsForSuccess(incomingOperations, senderAccKP.Address(), &paymentOp)
	assert.False(t, ok)
	incomingOperations[1], incomingOperations[0] = incomingOperations[0], incomingOperations[1] // Swap back ops.

	//TEST if payment op in incoming array of ops has source account is the same as the destination.
	incomingOperations[2] = &txnbuild.Payment{
		Destination:   senderAccKP.Address(),
		SourceAccount: senderAccKP.Address(),
		Amount:        "1",
		Asset:         assetGOAT,
	}
	ok = h.validateIncomingOperationsForSuccess(incomingOperations, senderAccKP.Address(), &paymentOp)
	assert.False(t, ok)

	//TEST if payment op in incoming array of ops has source account is not same as the first AllowTrust trustor.
	incomingOperations[2] = &txnbuild.Payment{
		Destination:   senderAccKP.Address(),
		SourceAccount: receiverAccKP.Address(),
		Amount:        "1",
		Asset:         assetGOAT,
	}
	ok = h.validateIncomingOperationsForSuccess(incomingOperations, senderAccKP.Address(), &paymentOp)
	assert.False(t, ok)

	//TEST if payment op in incoming array of ops has destination account is not same as the second AllowTrust trustor.
	incomingOperations[2] = &txnbuild.Payment{
		Destination:   senderAccKP.Address(),
		SourceAccount: receiverAccKP.Address(),
		Amount:        "1",
		Asset:         assetGOAT,
	}
	ok = h.validateIncomingOperationsForSuccess(incomingOperations, senderAccKP.Address(), &paymentOp)
	assert.False(t, ok)
}

func TestTxApproveHandlerHandleSuccessResponseIfNeeded_Success(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	defer db.Close()
	conn := db.Open()
	defer conn.Close()

	// Perpare accounts keypairs and source account mock horizon AccountDetail response.
	issuerAccKeyPair := keypair.MustRandom()
	senderAccKP := keypair.MustRandom()
	receiverAccKP := keypair.MustRandom()
	assetGOAT := txnbuild.CreditAsset{
		Code:   "GOAT",
		Issuer: issuerAccKeyPair.Address(),
	}
	horizonMock := horizonclient.MockClient{}
	horizonMock.
		On("AccountDetail", horizonclient.AccountRequest{AccountID: senderAccKP.Address()}).
		Return(horizon.Account{
			AccountID: senderAccKP.Address(),
			Sequence:  "2",
		}, nil)

	// Create tx-approve/ txApproveHandler.
	kycThresholdAmount, err := amount.ParseInt64("500")
	require.NoError(t, err)
	handler := txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         assetGOAT.GetCode(),
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
		db:                conn,
		kycThreshold:      kycThresholdAmount,
		baseURL:           "https://sep8-server.test",
	}

	// Build a compliant transaction.
	// Note on assetNoIssuerGOAT: AllowTrustOp only stores the AssetCode (4- or 12-char string),but does not store the issuer.
	// Since the issuer won't be in the encoded XDR we need to create a CreditAsset(which is one without an issuer).
	// This is the how the compliant transaction will behave after it's been parsed from the request.
	assetNoIssuerGOAT := txnbuild.CreditAsset{
		Code:   "GOAT",
		Issuer: "",
	}
	senderAcc, err := handler.horizonClient.AccountDetail(horizonclient.AccountRequest{AccountID: senderAccKP.Address()})
	compliantTxOps := []txnbuild.Operation{
		&txnbuild.AllowTrust{
			Trustor:       senderAccKP.Address(),
			Type:          assetNoIssuerGOAT,
			Authorize:     true,
			SourceAccount: issuerAccKeyPair.Address(),
		},
		&txnbuild.AllowTrust{
			Trustor:       receiverAccKP.Address(),
			Type:          assetNoIssuerGOAT,
			Authorize:     true,
			SourceAccount: issuerAccKeyPair.Address(),
		},
		&txnbuild.Payment{
			SourceAccount: senderAccKP.Address(),
			Destination:   receiverAccKP.Address(),
			Amount:        "1",
			Asset:         assetGOAT,
		},
		&txnbuild.AllowTrust{
			Trustor:       receiverAccKP.Address(),
			Type:          assetNoIssuerGOAT,
			Authorize:     false,
			SourceAccount: issuerAccKeyPair.Address(),
		},
		&txnbuild.AllowTrust{
			Trustor:       senderAccKP.Address(),
			Type:          assetNoIssuerGOAT,
			Authorize:     false,
			SourceAccount: issuerAccKeyPair.Address(),
		},
	}
	compliantTx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &senderAcc,
		IncrementSequenceNum: true,
		Operations:           compliantTxOps,
		BaseFee:              300,
		Timebounds:           txnbuild.NewTimeout(300),
	})
	require.NoError(t, err)

	// TEST success response.
	require.NoError(t, err)
	compliantResponse, err := handler.handleSuccessResponseIfNeeded(ctx, compliantTx)
	require.NoError(t, err)
	wantSuccessResponse := txApprovalResponse{
		Status:     sep8Status("success"),
		Tx:         compliantResponse.Tx,
		Message:    `Transaction is compliant and signed by the issuer.`,
		StatusCode: http.StatusOK,
	}
	assert.Equal(t, &wantSuccessResponse, compliantResponse)
}

func TestTxApproveHandlerHandleSuccessResponseIfNeeded_RevisableOrRejected(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	defer db.Close()
	conn := db.Open()
	defer conn.Close()

	// Perpare accounts keypairs and source account mock horizon AccountDetail response.
	issuerAccKeyPair := keypair.MustRandom()
	senderAccKP := keypair.MustRandom()
	receiverAccKP := keypair.MustRandom()
	assetGOAT := txnbuild.CreditAsset{
		Code:   "GOAT",
		Issuer: issuerAccKeyPair.Address(),
	}
	horizonMock := horizonclient.MockClient{}
	horizonMock.
		On("AccountDetail", horizonclient.AccountRequest{AccountID: senderAccKP.Address()}).
		Return(horizon.Account{
			AccountID: senderAccKP.Address(),
			Sequence:  "2",
		}, nil)

	// Create tx-approve/ txApproveHandler.
	kycThresholdAmount, err := amount.ParseInt64("500")
	require.NoError(t, err)
	handler := txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         assetGOAT.GetCode(),
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
		db:                conn,
		kycThreshold:      kycThresholdAmount,
		baseURL:           "https://sep8-server.test",
	}

	// Build a revisable transaction.
	// Note on assetNoIssuerGOAT: AllowTrustOp only stores the AssetCode (4- or 12-char string),but does not store the issuer.
	// Since the issuer won't be in the encoded XDR we need to create a CreditAsset(which is one without an issuer).
	// This is the how the compliant transaction will behave after it's been parsed from the request.
	assetNoIssuerGOAT := txnbuild.CreditAsset{
		Code:   "GOAT",
		Issuer: "",
	}
	senderAcc, err := handler.horizonClient.AccountDetail(horizonclient.AccountRequest{AccountID: senderAccKP.Address()})
	revisableTx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &senderAcc,
		IncrementSequenceNum: true,
		Operations: []txnbuild.Operation{
			&txnbuild.Payment{
				SourceAccount: senderAccKP.Address(),
				Destination:   receiverAccKP.Address(),
				Amount:        "1",
				Asset:         assetGOAT,
			},
		},
		BaseFee:    300,
		Timebounds: txnbuild.NewTimeout(300),
	})
	require.NoError(t, err)

	// TEST a noncompliant but revisable transaction.
	revisedResponse, err := handler.handleSuccessResponseIfNeeded(ctx, revisableTx)
	require.NoError(t, err)
	assert.Nil(t, revisedResponse)

	// Build a noncompliant transaction where the payment op is in the incorrect position.
	noncompliantTxOps := []txnbuild.Operation{
		&txnbuild.AllowTrust{
			Trustor:       senderAccKP.Address(),
			Type:          assetNoIssuerGOAT,
			Authorize:     true,
			SourceAccount: issuerAccKeyPair.Address(),
		},
		&txnbuild.Payment{
			SourceAccount: senderAccKP.Address(),
			Destination:   receiverAccKP.Address(),
			Amount:        "1",
			Asset:         assetGOAT,
		},
		&txnbuild.AllowTrust{
			Trustor:       receiverAccKP.Address(),
			Type:          assetNoIssuerGOAT,
			Authorize:     true,
			SourceAccount: issuerAccKeyPair.Address(),
		},
		&txnbuild.AllowTrust{
			Trustor:       receiverAccKP.Address(),
			Type:          assetNoIssuerGOAT,
			Authorize:     false,
			SourceAccount: issuerAccKeyPair.Address(),
		},
		&txnbuild.AllowTrust{
			Trustor:       senderAccKP.Address(),
			Type:          assetNoIssuerGOAT,
			Authorize:     false,
			SourceAccount: issuerAccKeyPair.Address(),
		},
	}
	noncompliantTx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &senderAcc,
		IncrementSequenceNum: true,
		Operations:           noncompliantTxOps,
		BaseFee:              300,
		Timebounds:           txnbuild.NewTimeout(300),
	})
	require.NoError(t, err)

	// TEST rejected response; nonauthorized operation(s).
	rejectedResponse, err := handler.handleSuccessResponseIfNeeded(ctx, noncompliantTx)
	require.NoError(t, err)
	wantRejectedResponse := txApprovalResponse{
		Status:     "rejected",
		Error:      "There is one or more unauthorized operations in the provided transaction.",
		StatusCode: http.StatusBadRequest,
	}
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)

	// Build a noncompliant transaction; where the last two AllowTrust ops do not deauthorize.
	noncompliantTxOps = []txnbuild.Operation{
		&txnbuild.AllowTrust{
			Trustor:       senderAccKP.Address(),
			Type:          assetNoIssuerGOAT,
			Authorize:     true,
			SourceAccount: issuerAccKeyPair.Address(),
		},
		&txnbuild.AllowTrust{
			Trustor:       receiverAccKP.Address(),
			Type:          assetNoIssuerGOAT,
			Authorize:     true,
			SourceAccount: issuerAccKeyPair.Address(),
		},
		&txnbuild.Payment{
			SourceAccount: senderAccKP.Address(),
			Destination:   receiverAccKP.Address(),
			Amount:        "1",
			Asset:         assetGOAT,
		},
		&txnbuild.AllowTrust{
			Trustor:       receiverAccKP.Address(),
			Type:          assetNoIssuerGOAT,
			Authorize:     true,
			SourceAccount: issuerAccKeyPair.Address(),
		},
		&txnbuild.AllowTrust{
			Trustor:       senderAccKP.Address(),
			Type:          assetNoIssuerGOAT,
			Authorize:     true,
			SourceAccount: issuerAccKeyPair.Address(),
		},
	}
	noncompliantTx, err = txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &senderAcc,
		IncrementSequenceNum: true,
		Operations:           noncompliantTxOps,
		BaseFee:              300,
		Timebounds:           txnbuild.NewTimeout(300),
	})

	// TEST rejected response nonauthorized ops.
	rejectedResponse, err = handler.handleSuccessResponseIfNeeded(ctx, noncompliantTx)
	require.NoError(t, err)
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)
}

func TestTxApproveHandlerHandleSuccessResponseIfNeeded_KYCRequired(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	defer db.Close()
	conn := db.Open()
	defer conn.Close()

	// Perpare accounts keypairs and source account mock horizon AccountDetail response.
	issuerAccKeyPair := keypair.MustRandom()
	senderAccKP := keypair.MustRandom()
	receiverAccKP := keypair.MustRandom()
	assetGOAT := txnbuild.CreditAsset{
		Code:   "GOAT",
		Issuer: issuerAccKeyPair.Address(),
	}
	horizonMock := horizonclient.MockClient{}
	horizonMock.
		On("AccountDetail", horizonclient.AccountRequest{AccountID: senderAccKP.Address()}).
		Return(horizon.Account{
			AccountID: senderAccKP.Address(),
			Sequence:  "2",
		}, nil)

	// Create tx-approve/ txApproveHandler.
	kycThresholdAmount, err := amount.ParseInt64("500")
	require.NoError(t, err)
	handler := txApproveHandler{
		issuerKP:          issuerAccKeyPair,
		assetCode:         assetGOAT.GetCode(),
		horizonClient:     &horizonMock,
		networkPassphrase: network.TestNetworkPassphrase,
		db:                conn,
		kycThreshold:      kycThresholdAmount,
		baseURL:           "https://sep8-server.test",
	}

	// Build a compliant transaction where the payment op exceeds the kycThreshold.
	// Note on assetNoIssuerGOAT: AllowTrustOp only stores the AssetCode (4- or 12-char string),but does not store the issuer.
	// Since the issuer won't be in the encoded XDR we need to create a CreditAsset(which is one without an issuer).
	// This is the how the compliant transaction will behave after it's been parsed from the request.
	assetNoIssuerGOAT := txnbuild.CreditAsset{
		Code:   "GOAT",
		Issuer: "",
	}
	senderAcc, err := handler.horizonClient.AccountDetail(horizonclient.AccountRequest{AccountID: senderAccKP.Address()})
	kycReqCompliantTxOps := []txnbuild.Operation{
		&txnbuild.AllowTrust{
			Trustor:       senderAccKP.Address(),
			Type:          assetNoIssuerGOAT,
			Authorize:     true,
			SourceAccount: issuerAccKeyPair.Address(),
		},
		&txnbuild.AllowTrust{
			Trustor:       receiverAccKP.Address(),
			Type:          assetNoIssuerGOAT,
			Authorize:     true,
			SourceAccount: issuerAccKeyPair.Address(),
		},
		&txnbuild.Payment{
			SourceAccount: senderAccKP.Address(),
			Destination:   receiverAccKP.Address(),
			Amount:        "501",
			Asset:         assetGOAT,
		},
		&txnbuild.AllowTrust{
			Trustor:       receiverAccKP.Address(),
			Type:          assetNoIssuerGOAT,
			Authorize:     false,
			SourceAccount: issuerAccKeyPair.Address(),
		},
		&txnbuild.AllowTrust{
			Trustor:       senderAccKP.Address(),
			Type:          assetNoIssuerGOAT,
			Authorize:     false,
			SourceAccount: issuerAccKeyPair.Address(),
		},
	}
	kycReqCompliantTx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &senderAcc,
		IncrementSequenceNum: true,
		Operations:           kycReqCompliantTxOps,
		BaseFee:              300,
		Timebounds:           txnbuild.NewTimeout(300),
	})
	require.NoError(t, err)

	// TEST action required response KYC required.
	actionRequiredResponse, err := handler.handleSuccessResponseIfNeeded(ctx, kycReqCompliantTx)
	require.NoError(t, err)
	wantActionRequiredResponse := txApprovalResponse{
		Status:       sep8Status("action_required"),
		Message:      `Payments exceeding 500.00 GOAT requires KYC approval. Please provide an email address.`,
		StatusCode:   http.StatusOK,
		ActionURL:    actionRequiredResponse.ActionURL,
		ActionMethod: "POST",
		ActionFields: []string{"email_address"},
	}
	assert.Equal(t, &wantActionRequiredResponse, actionRequiredResponse)

	// TEST rejected response KYC rejected compliant transaction.
	updateAccountKycQuery := `
	UPDATE accounts_kyc_status
	SET kyc_submitted_at = NOW(), email_address = $1, approved_at = NULL, rejected_at = NOW()
	WHERE stellar_address = $2
	`
	_, err = handler.db.ExecContext(ctx, updateAccountKycQuery, "xEmail@test.com", senderAccKP.Address())
	require.NoError(t, err)
	rejectedResponse, err := handler.handleSuccessResponseIfNeeded(ctx, kycReqCompliantTx)
	require.NoError(t, err)
	wantRejectedResponse := txApprovalResponse{
		Status:     "rejected",
		Error:      "Your KYC was rejected and you're not authorized for operations above 500.00 GOAT.",
		StatusCode: http.StatusBadRequest,
	}
	assert.Equal(t, &wantRejectedResponse, rejectedResponse)

	// TEST success response KYC approved and is a compliant transaction.
	updateAccountKycQuery = `
	UPDATE accounts_kyc_status
	SET kyc_submitted_at = NOW(), email_address = $1, approved_at = NOW(), rejected_at = NULL
	WHERE stellar_address = $2
	`
	_, err = handler.db.ExecContext(ctx, updateAccountKycQuery, "Email@test.com", senderAccKP.Address())
	require.NoError(t, err)
	successApprovedResponse, err := handler.handleSuccessResponseIfNeeded(ctx, kycReqCompliantTx)
	require.NoError(t, err)
	wantSuccessResponse := txApprovalResponse{
		Status:     sep8Status("success"),
		Tx:         successApprovedResponse.Tx,
		Message:    `Transaction is compliant and signed by the issuer.`,
		StatusCode: http.StatusOK,
	}
	assert.Equal(t, &wantSuccessResponse, successApprovedResponse)
}
