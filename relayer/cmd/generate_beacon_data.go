package cmd

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/cbroglie/mustache"
	"github.com/snowfork/go-substrate-rpc-client/v4/types"

	log "github.com/sirupsen/logrus"
	"github.com/snowfork/snowbridge/relayer/relays/beacon/cache"
	"github.com/snowfork/snowbridge/relayer/relays/beacon/config"
	"github.com/snowfork/snowbridge/relayer/relays/beacon/header/syncer"
	beaconjson "github.com/snowfork/snowbridge/relayer/relays/beacon/header/syncer/json"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func generateBeaconDataCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "generate-beacon-data",
		Short: "Generate beacon data.",
		Args:  cobra.ExactArgs(0),
		RunE:  generateBeaconData,
	}

	cmd.Flags().String("spec", "", "Valid values are mainnet or minimal")
	err := cmd.MarkFlagRequired("spec")
	if err != nil {
		return nil
	}

	cmd.Flags().String("url", "http://127.0.0.1:9596", "Beacon URL")
	if err != nil {
		return nil
	}

	return cmd
}

func generateBeaconCheckpointCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "generate-beacon-checkpoint",
		Short: "Generate beacon checkpoint.",
		Args:  cobra.ExactArgs(0),
		RunE:  generateBeaconCheckpoint,
	}

	cmd.Flags().String("spec", "", "Valid values are mainnet or minimal")
	err := cmd.MarkFlagRequired("spec")
	if err != nil {
		return nil
	}

	cmd.Flags().String("url", "http://127.0.0.1:9596", "Beacon URL")
	if err != nil {
		return nil
	}

	cmd.Flags().Bool("export-json", true, "Export Json")
	if err != nil {
		return nil
	}

	return cmd
}

type Data struct {
	CheckpointUpdate      beaconjson.CheckPoint
	SyncCommitteeUpdate   beaconjson.Update
	FinalizedHeaderUpdate beaconjson.Update
	HeaderUpdate          beaconjson.HeaderUpdate
}

const (
	pathToBeaconBenchmarkData    = "parachain/pallets/ethereum-beacon-client/src/benchmarking"
	pathToBenchmarkDataTemplate  = "parachain/templates/benchmark-fixtures.mustache"
	pathToBeaconTestFixtureFiles = "parachain/pallets/ethereum-beacon-client/tests/fixtures"
)

func generateBeaconCheckpoint(cmd *cobra.Command, _ []string) error {
	err := func() error {
		spec, err := cmd.Flags().GetString("spec")
		if err != nil {
			return fmt.Errorf("get active spec: %w", err)
		}

		activeSpec, err := config.ToSpec(spec)
		if err != nil {
			return fmt.Errorf("get spec: %w", err)
		}

		endpoint, err := cmd.Flags().GetString("url")

		viper.SetConfigFile("core/packages/test/config/beacon-relay.json")

		if err := viper.ReadInConfig(); err != nil {
			return err
		}

		var conf config.Config
		err = viper.Unmarshal(&conf)
		if err != nil {
			return err
		}

		specSettings := conf.GetSpecSettingsBySpec(activeSpec)

		s := syncer.New(endpoint, specSettings.SlotsInEpoch, specSettings.EpochsPerSyncCommitteePeriod, specSettings.MaxSlotsPerHistoricalRoot, activeSpec)

		checkPointScale, err := s.GetCheckpoint()
		if err != nil {
			return fmt.Errorf("get initial sync: %w", err)
		}
		exportJson, err := cmd.Flags().GetBool("export_json")
		if exportJson {
			initialSync := checkPointScale.ToJSON()
			err = writeJSONToFile(initialSync, "dump-initial-checkpoint.json")
			if err != nil {
				return fmt.Errorf("write initial sync to file: %w", err)
			}
		}
		checkPointBytes, _ := types.EncodeToBytes(checkPointScale)
		// Call index for EthereumBeaconClient.force_checkpoint
		checkPointCallIndex := "0x3200"
		checkPointUpdateCall := checkPointCallIndex + hex.EncodeToString(checkPointBytes)
		fmt.Println(checkPointUpdateCall)
		return nil
	}()
	if err != nil {
		log.WithError(err).Error("error generating beacon checkpoint")
	}

	return nil
}

func generateBeaconData(cmd *cobra.Command, _ []string) error {
	err := func() error {
		spec, err := cmd.Flags().GetString("spec")
		if err != nil {
			return fmt.Errorf("get active spec: %w", err)
		}

		activeSpec, err := config.ToSpec(spec)
		if err != nil {
			return fmt.Errorf("get spec: %w", err)
		}

		endpoint, err := cmd.Flags().GetString("url")

		viper.SetConfigFile("core/packages/test/config/beacon-relay.json")
		if err := viper.ReadInConfig(); err != nil {
			return err
		}

		var conf config.Config
		err = viper.Unmarshal(&conf)
		if err != nil {
			return err
		}

		specSettings := conf.GetSpecSettingsBySpec(activeSpec)

		log.WithFields(log.Fields{"spec": activeSpec, "endpoint": endpoint}).Info("connecting to beacon API")

		s := syncer.New(endpoint, specSettings.SlotsInEpoch, specSettings.EpochsPerSyncCommitteePeriod, specSettings.MaxSlotsPerHistoricalRoot, activeSpec)

		initialSyncScale, err := s.GetCheckpoint()
		if err != nil {
			return fmt.Errorf("get initial sync: %w", err)
		}
		initialSync := initialSyncScale.ToJSON()
		err = writeJSONToFile(initialSync, fmt.Sprintf("initial-checkpoint.%s.json", activeSpec.ToString()))
		initialSyncHeaderSlot := initialSync.Header.Slot
		initialSyncPeriod := s.ComputeSyncPeriodAtSlot(initialSyncHeaderSlot)
		log.Info("created initial sync file")

		log.Info("downloading beacon state, this can take a few minutes...")
		// wait for 5 blocks
		time.Sleep(6 * time.Second * 5)
		syncCommitteePeriod := s.ComputeSyncPeriodAtSlot(initialSyncHeaderSlot + 5)
		if initialSyncPeriod != syncCommitteePeriod {
			return fmt.Errorf("initialSyncPeriod %d should be consistent with syncCommitteePeriod %d", initialSyncPeriod, syncCommitteePeriod)
		}

		syncCommitteeUpdateScale, err := s.GetSyncCommitteePeriodUpdate(syncCommitteePeriod)
		if err != nil {
			return fmt.Errorf("get sync committee update: %w", err)
		}
		syncCommitteeUpdate := syncCommitteeUpdateScale.Payload.ToJSON()

		err = writeJSONToFile(syncCommitteeUpdate, fmt.Sprintf("sync-committee-update.%s.json", activeSpec.ToString()))
		if err != nil {
			return fmt.Errorf("write sync committee update to file: %w", err)
		}
		log.Info("created sync committee update file")

		log.Info("downloading beacon state, this can take a few minutes...")
		finalizedUpdateScale, err := s.GetFinalizedUpdate()
		if err != nil {
			return fmt.Errorf("get finalized header update: %w", err)
		}
		finalizedUpdate := finalizedUpdateScale.Payload.ToJSON()
		err = writeJSONToFile(finalizedUpdate, fmt.Sprintf("finalized-header-update.%s.json", activeSpec.ToString()))
		if err != nil {
			return fmt.Errorf("write finalized header update to file: %w", err)
		}
		log.Info("created finalized header update file")

		finalizedUpdatePeriod := s.ComputeSyncPeriodAtSlot(finalizedUpdate.SignatureSlot)
		if initialSyncPeriod != finalizedUpdatePeriod {
			return fmt.Errorf("initialSyncPeriod %d should be consistent with finalizedUpdatePeriod %d", initialSyncPeriod, finalizedUpdatePeriod)
		}
		if finalizedUpdate.AttestedHeader.Slot <= initialSyncHeaderSlot {
			return fmt.Errorf("AttestedHeader slot %d should be greater than initialSyncHeaderSlot %d", finalizedUpdate.AttestedHeader.Slot, initialSyncHeaderSlot)
		}

		blockUpdateSlot := uint64(finalizedUpdateScale.Payload.FinalizedHeader.Slot - 2)
		checkPoint := cache.Proof{
			FinalizedBlockRoot: finalizedUpdateScale.FinalizedHeaderBlockRoot,
			BlockRootsTree:     finalizedUpdateScale.BlockRootsTree,
			Slot:               uint64(finalizedUpdateScale.Payload.FinalizedHeader.Slot),
		}
		headerUpdateScale, err := s.GetNextHeaderUpdateBySlotWithAncestryProof(blockUpdateSlot, checkPoint)
		if err != nil {
			return fmt.Errorf("get header update: %w", err)
		}
		if err != nil {
			return fmt.Errorf("get next header update to get sync aggregate: %w", err)
		}
		headerUpdate := headerUpdateScale.ToJSON()
		err = writeJSONToFile(headerUpdate, fmt.Sprintf("execution-header-update.%s.json", activeSpec.ToString()))
		if err != nil {
			return fmt.Errorf("write block update to file: %w", err)
		}

		log.Info("created header update file")

		if activeSpec.IsMainnet() {
			log.Info("now updating benchmarking data files")

			// Rust file hexes require the 0x of hashes to be removed
			initialSync.RemoveLeadingZeroHashes()
			syncCommitteeUpdate.RemoveLeadingZeroHashes()
			finalizedUpdate.RemoveLeadingZeroHashes()
			headerUpdate.RemoveLeadingZeroHashes()

			data := Data{
				CheckpointUpdate:      initialSync,
				SyncCommitteeUpdate:   syncCommitteeUpdate,
				FinalizedHeaderUpdate: finalizedUpdate,
				HeaderUpdate:          headerUpdate,
			}

			log.WithFields(log.Fields{
				"location": pathToBeaconTestFixtureFiles,
				"spec":     activeSpec,
			}).Info("rendering file using mustache")

			rendered, err := mustache.RenderFile(pathToBenchmarkDataTemplate, data)
			filename := "fixtures.rs"

			log.WithFields(log.Fields{
				"location": pathToBeaconBenchmarkData,
				"filename": filename,
			}).Info("writing result file")

			err = writeBenchmarkDataFile(filename, rendered)
			if err != nil {
				return err
			}
		}

		log.WithField("spec", activeSpec).Info("done")

		return nil
	}()
	if err != nil {
		log.WithError(err).Error("error generating beacon data")
	}

	return nil
}

func writeJSONToFile(data interface{}, filename string) error {
	file, _ := json.MarshalIndent(data, "", "  ")

	f, err := os.OpenFile(fmt.Sprintf("%s/%s", pathToBeaconTestFixtureFiles, filename), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)

	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}

	defer f.Close()

	_, err = f.Write(file)

	if err != nil {
		return fmt.Errorf("write to file: %w", err)
	}

	return nil
}

func writeBenchmarkDataFile(filename, fileContents string) error {
	f, err := os.OpenFile(fmt.Sprintf("%s/%s", pathToBeaconBenchmarkData, filename), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)

	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}

	defer f.Close()

	_, err = f.Write([]byte(fileContents))

	if err != nil {
		return fmt.Errorf("write to file: %w", err)
	}

	return nil
}