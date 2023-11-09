package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/uptrace/bun"

	"github.com/getzep/zep/pkg/models"
	"github.com/getzep/zep/pkg/search"
)

// getSimpleMemory returns the most recent Summary and a list of messages for a given sessionID.
// getSimpleMemory returns:
//   - the most recent Summary, if one exists
//   - the lastNMessages messages, if lastNMessages > 0
//   - all messages since the last SummaryPoint, if lastNMessages == 0
//   - if no Summary (and no SummaryPoint) exists and lastNMessages == 0, returns
//     all undeleted messages up to the configured message window
func getSimpleMemory(
	ctx context.Context,
	db *bun.DB,
	appState *models.AppState,
	config *models.MemoryConfig,
) (*models.Memory, error) {
	sessionID := config.SessionID
	lastNMessages := config.LastNMessages

	if lastNMessages < 0 {
		return nil, errors.New("cannot specify negative lastNMessages")
	}

	// Get the most recent summary
	summary, err := getSummary(ctx, db, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get summary %w", err)
	}
	if summary != nil {
		log.Debugf("Got summary for %s: %s", sessionID, summary.UUID)
	}

	// get the messages
	messages, err := getMessages(
		ctx,
		db,
		sessionID,
		appState.Config.Memory.MessageWindow,
		summary,
		lastNMessages,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages: %w", err)
	}
	if messages != nil {
		log.Debugf("Got messages for %s: %d", sessionID, len(messages))
	}

	return &models.Memory{
		Messages: messages,
		Summary:  summary,
	}, nil
}

// getPerpetualMemory returns the most recent Summary and a list of messages for a given sessionID.
func getPerpetualMemory(
	ctx context.Context,
	db *bun.DB,
	appState *models.AppState,
	config *models.MemoryConfig) (*models.Memory, error) {
	var currentSummary *models.Summary
	var currentSummaryErr error

	wg := &sync.WaitGroup{}

	if config.SessionID == "" {
		return nil, errors.New("sessionID cannot be empty")
	}

	if config.LastNMessages < 1 {
		return nil, errors.New("lastNMessages must be greater than 0")
	}

	// Get current summary in the background
	if config.IncludeCurrentSummary {
		wg.Add(1)
		go func() {
			defer wg.Done()

			var err error
			currentSummary, err = getSummary(ctx, db, config.SessionID)
			if err != nil {
				currentSummaryErr = fmt.Errorf("failed to get summary %w", err)
			}
			if currentSummary != nil {
				log.Debugf("Got summary for %s: %s", config.SessionID, currentSummary.UUID)
			}
		}()
	}

	// Get the last N messages
	messages, err := getMessages(
		ctx,
		db,
		config.SessionID,
		appState.Config.Memory.MessageWindow,
		nil,
		config.LastNMessages,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages: %w", err)
	}

	// Search summaries
	retriever := search.NewMultiQuestionSummaryRetriever(
		appState,
		config.SessionID,
		3, // amke this a constant
		messages,
		appState.Config.LLM.Service,
	)

	summaries, err := retriever.Run(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve summaries: %w", err)
	}

	if currentSummaryErr != nil {
		return nil, currentSummaryErr
	}

	return &models.Memory{
		Messages:  messages,
		Summary:   currentSummary,
		Summaries: summaries,
	}, nil
}