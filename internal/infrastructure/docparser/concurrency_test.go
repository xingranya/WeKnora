package docparser

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGateParserLimitsAndReportsLocalRuntime(t *testing.T) {
	ConfigureConcurrency(nil)
	_, first, err := GateParser(context.Background(), MinerUEngineName, 1)
	require.NoError(t, err)
	t.Cleanup(first)

	acquired := make(chan func(), 1)
	go func() {
		_, release, _ := GateParser(context.Background(), MinerUEngineName, 1)
		acquired <- release
	}()

	require.Eventually(t, func() bool {
		stats, available, err := ParserConcurrencyStats(context.Background())
		if err != nil || !available || len(stats) != 1 {
			return false
		}
		return stats[0].ModelID == "parser:"+MinerUEngineName &&
			stats[0].Active == 1 && stats[0].Waiting == 1 && stats[0].Limit == 1
	}, time.Second, 10*time.Millisecond)

	select {
	case release := <-acquired:
		release()
		t.Fatal("并发槽位未释放前，等待任务不应通过")
	default:
	}

	first()
	select {
	case release := <-acquired:
		release()
	case <-time.After(time.Second):
		t.Fatal("释放并发槽位后，等待任务应继续执行")
	}
}
