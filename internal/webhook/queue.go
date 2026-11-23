package webhook

import (
	"context"
	"errors"
	"time"

	"github.com/Kayyo321/taaro/internal/store"
)

// Drain schedules one worker for due deliveries. A concurrent enqueue requests
// another pass so a delivery cannot be missed as an idle worker exits.
func (h *Handler) Drain() {
	h.queueMu.Lock()
	defer h.queueMu.Unlock()
	if h.draining {
		h.wake = true
		return
	}
	h.draining = true
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		for {
			h.processQueue()
			h.queueMu.Lock()
			if !h.wake {
				h.draining = false
				h.queueMu.Unlock()
				return
			}
			h.wake = false
			h.queueMu.Unlock()
		}
	}()
}

func (h *Handler) processQueue() {
	defer h.drainHandoffRefresh()
	defer h.drainProposalAwarenessRefresh()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		d, err := h.Store.NextDelivery(ctx, time.Now())
		if err != nil {
			cancel()
			if !errors.Is(err, store.ErrNotFound) {
				h.logError("cannot read webhook queue", "err", err)
			}
			return
		}
		err = h.processEvent(ctx, d.DeliveryID, d.Event, d.Payload, d.ReceivedAt)
		cancel()
		if err != nil {
			h.logError("webhook processing failed; retry scheduled", "delivery_id", d.DeliveryID, "err", err)
		}
		if err = h.Store.FinishDelivery(context.Background(), d.DeliveryID, err == nil); err != nil {
			h.logError("cannot update webhook queue", "err", err)
			return
		}
	}
}
