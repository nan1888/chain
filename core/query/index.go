package query

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/lib/pq"

	"chain/database/pg"
	"chain/errors"
	"chain/protocol/bc"
)

const (
	// TxPinName is used to identify the pin associated
	// with the transaction block processor.
	TxPinName = "tx"
)

// Annotator describes a function capable of adding annotations
// to transactions, inputs and outputs.
type Annotator func(ctx context.Context, txs []*AnnotatedTx) error

// RegisterAnnotator adds an additional annotator capable of mutating
// the annotated transaction object.
func (ind *Indexer) RegisterAnnotator(annotator Annotator) {
	ind.annotators = append(ind.annotators, annotator)
}

func (ind *Indexer) ProcessBlocks(ctx context.Context) {
	if ind.pinStore == nil {
		return
	}
	ind.pinStore.ProcessBlocks(ctx, ind.c, TxPinName, ind.IndexTransactions)
}

// IndexTransactions is registered as a block callback on the Chain. It
// saves all annotated transactions to the database.
func (ind *Indexer) IndexTransactions(ctx context.Context, b *bc.Block) error {
	<-ind.pinStore.PinWaiter("asset", b.Height)

	err := ind.insertBlock(ctx, b)
	if err != nil {
		return err
	}
	txs, err := ind.insertAnnotatedTxs(ctx, b)
	if err != nil {
		return err
	}
	err = ind.insertAnnotatedOutputs(ctx, b, txs)
	if err != nil {
		return err
	}
	err = ind.insertAnnotatedInputs(ctx, b, txs)
	return err
}

func (ind *Indexer) insertBlock(ctx context.Context, b *bc.Block) error {
	const q = `
		INSERT INTO query_blocks (height, timestamp) VALUES($1, $2)
		ON CONFLICT (height) DO NOTHING
	`
	_, err := ind.db.Exec(ctx, q, b.Height, b.TimestampMS)
	return errors.Wrap(err, "inserting block timestamp")
}

func (ind *Indexer) insertAnnotatedTxs(ctx context.Context, b *bc.Block) ([]*AnnotatedTx, error) {
	var (
		hashes           = pq.ByteaArray(make([][]byte, 0, len(b.Transactions)))
		positions        = pg.Uint32s(make([]uint32, 0, len(b.Transactions)))
		annotatedTxBlobs = pq.StringArray(make([]string, 0, len(b.Transactions)))
		annotatedTxs     = make([]*AnnotatedTx, 0, len(b.Transactions))
		locals           = pq.BoolArray(make([]bool, 0, len(b.Transactions)))
		referenceDatas   = pq.StringArray(make([]string, 0, len(b.Transactions)))
		outputIDs        = pq.ByteaArray(make([][]byte, 0))
	)
	for _, tx := range b.Transactions {
		for _, in := range tx.Inputs {
			if !in.IsIssuance() {
				outputIDs = append(outputIDs, in.SpentOutputID().Bytes())
			}
		}
	}
	outpoints, err := ind.loadOutpoints(ctx, outputIDs)
	if err != nil {
		return nil, err
	}

	// Build the fully annotated transactions.
	for pos, tx := range b.Transactions {
		annotatedTxs = append(annotatedTxs, buildAnnotatedTransaction(tx, b, uint32(pos), outpoints))
	}
	for _, annotator := range ind.annotators {
		err = annotator(ctx, annotatedTxs)
		if err != nil {
			return nil, errors.Wrap(err, "adding external annotations")
		}
	}
	localAnnotator(ctx, annotatedTxs)

	// Collect the fields we need to commit to the DB.
	for pos, tx := range annotatedTxs {
		b, err := json.Marshal(tx)
		if err != nil {
			return nil, err
		}
		annotatedTxBlobs = append(annotatedTxBlobs, string(b))
		hashes = append(hashes, tx.ID[:])
		positions = append(positions, uint32(pos))
		locals = append(locals, bool(tx.IsLocal))
		referenceDatas = append(referenceDatas, string(*tx.ReferenceData))
	}

	// Save the annotated txs to the database.
	const insertQ = `
		INSERT INTO annotated_txs(block_height, block_id, timestamp,
			tx_pos, tx_hash, data, local, reference_data)
		SELECT $1, $2, $3, unnest($4::integer[]), unnest($5::bytea[]),
			unnest($6::jsonb[]), unnest($7::boolean[]), unnest($8::jsonb[])
		ON CONFLICT (block_height, tx_pos) DO NOTHING;
	`
	_, err = ind.db.Exec(ctx, insertQ, b.Height, b.Hash(), b.Time(), positions,
		hashes, annotatedTxBlobs, locals, referenceDatas)
	if err != nil {
		return nil, errors.Wrap(err, "inserting annotated_txs to db")
	}
	return annotatedTxs, nil
}

func (ind *Indexer) insertAnnotatedInputs(ctx context.Context, b *bc.Block, annotatedTxs []*AnnotatedTx) error {
	var (
		inputTxHashes         pq.ByteaArray
		inputIndexes          pq.Int64Array
		inputTypes            pq.StringArray
		inputAssetIDs         pq.ByteaArray
		inputAssetAliases     pq.StringArray
		inputAssetDefinitions pq.StringArray
		inputAssetTags        pq.StringArray
		inputAssetLocals      pq.BoolArray
		inputAmounts          pq.Int64Array
		inputAccountIDs       []sql.NullString
		inputAccountAliases   []sql.NullString
		inputAccountTags      []sql.NullString
		inputIssuancePrograms pq.ByteaArray
		inputReferenceDatas   pq.StringArray
		inputLocals           pq.BoolArray
	)

	for _, annotatedTx := range annotatedTxs {
		for i, in := range annotatedTx.Inputs {
			inputTxHashes = append(inputTxHashes, annotatedTx.ID[:])
			inputIndexes = append(inputIndexes, int64(i))
			inputTypes = append(inputTypes, in.Type)
			inputAssetIDs = append(inputAssetIDs, in.AssetID[:])
			inputAssetAliases = append(inputAssetAliases, in.AssetAlias)
			inputAssetDefinitions = append(inputAssetDefinitions, string(*in.AssetDefinition))
			inputAssetTags = append(inputAssetTags, string(*in.AssetTags))
			inputAssetLocals = append(inputAssetLocals, bool(in.AssetIsLocal))
			inputAmounts = append(inputAmounts, int64(in.Amount))
			inputAccountIDs = append(inputAccountIDs, sql.NullString{String: in.AccountID, Valid: in.AccountID != ""})
			inputAccountAliases = append(inputAccountAliases, sql.NullString{String: in.AccountAlias, Valid: in.AccountAlias != ""})
			if in.AccountTags != nil {
				inputAccountTags = append(inputAccountTags, sql.NullString{String: string(*in.AccountTags), Valid: true})
			} else {
				inputAccountTags = append(inputAccountTags, sql.NullString{})
			}
			inputIssuancePrograms = append(inputIssuancePrograms, in.IssuanceProgram)
			inputReferenceDatas = append(inputReferenceDatas, string(*in.ReferenceData))
			inputLocals = append(inputLocals, bool(in.IsLocal))
		}
	}
	const insertQ = `
		INSERT INTO annotated_inputs (tx_hash, index, type,
			asset_id, asset_alias, asset_definition, asset_tags, asset_local,
			amount, account_id, account_alias, account_tags, issuance_program,
			reference_data, local)
		SELECT unnest($1::bytea[]), unnest($2::integer[]), unnest($3::text[]), unnest($4::bytea[]),
		unnest($5::text[]), unnest($6::jsonb[]), unnest($7::jsonb[]), unnest($8::boolean[]),
		unnest($9::bigint[]), unnest($10::text[]), unnest($11::text[]), unnest($12::jsonb[]),
		unnest($13::bytea[]), unnest($14::jsonb[]), unnest($15::boolean[])
		ON CONFLICT (tx_hash, index) DO NOTHING;
	`
	_, err := ind.db.Exec(ctx, insertQ, inputTxHashes, inputIndexes, inputTypes, inputAssetIDs,
		inputAssetAliases, inputAssetDefinitions, pq.Array(inputAssetTags), inputAssetLocals,
		inputAmounts, pq.Array(inputAccountIDs), pq.Array(inputAccountAliases), pq.Array(inputAccountTags),
		inputIssuancePrograms, inputReferenceDatas, inputLocals)
	return errors.Wrap(err, "batch inserting annotated inputs")
}

func (ind *Indexer) loadOutpoints(ctx context.Context, outputIDs pq.ByteaArray) (map[bc.OutputID]bc.Outpoint, error) {
	const q = `
		SELECT tx_hash, output_index
		FROM annotated_outputs
		WHERE output_id IN (SELECT unnest($1::bytea[]))
	`
	results := make(map[bc.OutputID]bc.Outpoint)
	err := pg.ForQueryRows(ctx, ind.db, q, outputIDs, func(txHash bc.Hash, outputIndex uint32) {
		// We compute outid on the fly instead of receiving it from DB to save 47% of bandwidth:
		// DB is sending (hash256, int32) instead of (hash, int32, hash).
		outid := bc.ComputeOutputID(txHash, outputIndex)
		results[outid] = bc.Outpoint{
			Hash:  txHash,
			Index: outputIndex,
		}
	})
	if err != nil {
		return nil, errors.Wrap(err)
	}
	return results, nil
}

func (ind *Indexer) insertAnnotatedOutputs(ctx context.Context, b *bc.Block, annotatedTxs []*AnnotatedTx) error {
	var (
		outputIDs              pq.ByteaArray
		outputTxPositions      pg.Uint32s
		outputIndexes          pg.Uint32s
		outputTxHashes         pq.ByteaArray
		outputTypes            pq.StringArray
		outputPurposes         pq.StringArray
		outputAssetIDs         pq.ByteaArray
		outputAssetAliases     pq.StringArray
		outputAssetDefinitions pq.StringArray
		outputAssetTags        pq.StringArray
		outputAssetLocals      pq.BoolArray
		outputAmounts          pq.Int64Array
		outputAccountIDs       []sql.NullString
		outputAccountAliases   []sql.NullString
		outputAccountTags      []sql.NullString
		outputControlPrograms  pq.ByteaArray
		outputReferenceDatas   pq.StringArray
		outputLocals           pq.BoolArray
		prevoutIDs             pq.ByteaArray
	)

	for pos, tx := range b.Transactions {
		for _, in := range tx.Inputs {
			if !in.IsIssuance() {
				prevoutID := in.SpentOutputID()
				prevoutIDs = append(prevoutIDs, prevoutID.Bytes())
			}
		}

		for outIndex, out := range annotatedTxs[pos].Outputs {
			if out.Type == "retire" {
				continue
			}

			outputIDs = append(outputIDs, out.OutputID.Hash[:])
			outputTxPositions = append(outputTxPositions, uint32(pos))
			outputIndexes = append(outputIndexes, uint32(outIndex))
			outputTxHashes = append(outputTxHashes, tx.Hash[:])
			outputTypes = append(outputTypes, out.Type)
			outputPurposes = append(outputPurposes, out.Purpose)
			outputAssetIDs = append(outputAssetIDs, out.AssetID[:])
			outputAssetAliases = append(outputAssetAliases, out.AssetAlias)
			outputAssetDefinitions = append(outputAssetDefinitions, string(*out.AssetDefinition))
			outputAssetTags = append(outputAssetTags, string(*out.AssetTags))
			outputAssetLocals = append(outputAssetLocals, bool(out.AssetIsLocal))
			outputAmounts = append(outputAmounts, int64(out.Amount))
			outputAccountIDs = append(outputAccountIDs, sql.NullString{String: out.AccountID, Valid: out.AccountID != ""})
			outputAccountAliases = append(outputAccountAliases, sql.NullString{String: out.AccountAlias, Valid: out.AccountAlias != ""})
			if out.AccountTags != nil {
				outputAccountTags = append(outputAccountTags, sql.NullString{String: string(*out.AccountTags), Valid: true})
			} else {
				outputAccountTags = append(outputAccountTags, sql.NullString{})
			}
			outputControlPrograms = append(outputControlPrograms, out.ControlProgram)
			outputReferenceDatas = append(outputReferenceDatas, string(*out.ReferenceData))
			outputLocals = append(outputLocals, bool(out.IsLocal))
		}
	}

	// Insert all of the block's outputs at once.
	const insertQ = `
		INSERT INTO annotated_outputs (block_height, tx_pos, output_index, tx_hash,
			timespan, output_id, type, purpose, asset_id, asset_alias, asset_definition,
			asset_tags, asset_local, amount, account_id, account_alias, account_tags,
			control_program, reference_data, local)
		SELECT $1, unnest($2::integer[]), unnest($3::integer[]), unnest($4::bytea[]),
		int8range($5, NULL), unnest($6::bytea[]), unnest($7::text[]), unnest($8::text[]),
		unnest($9::bytea[]), unnest($10::text[]), unnest($11::jsonb[]), unnest($12::jsonb[]),
		unnest($13::boolean[]), unnest($14::bigint[]), unnest($15::text[]), unnest($16::text[]),
		unnest($17::jsonb[]), unnest($18::bytea[]), unnest($19::jsonb[]), unnest($20::boolean[])
		ON CONFLICT (block_height, tx_pos, output_index) DO NOTHING;
	`
	_, err := ind.db.Exec(ctx, insertQ, b.Height, outputTxPositions,
		outputIndexes, outputTxHashes, b.TimestampMS, outputIDs, outputTypes,
		outputPurposes, outputAssetIDs, outputAssetAliases,
		outputAssetDefinitions, outputAssetTags, outputAssetLocals,
		outputAmounts, pq.Array(outputAccountIDs), pq.Array(outputAccountAliases),
		pq.Array(outputAccountTags), outputControlPrograms, outputReferenceDatas,
		outputLocals)
	if err != nil {
		return errors.Wrap(err, "batch inserting annotated outputs")
	}

	const updateQ = `
		UPDATE annotated_outputs SET timespan = INT8RANGE(LOWER(timespan), $1)
		WHERE (output_id) IN (SELECT unnest($2::bytea[]))
	`
	_, err = ind.db.Exec(ctx, updateQ, b.TimestampMS, prevoutIDs)
	return errors.Wrap(err, "updating spent annotated outputs")
}
