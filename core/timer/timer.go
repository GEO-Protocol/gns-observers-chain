package timer

import (
	"geo-observers-blockchain/core/common"
	"geo-observers-blockchain/core/network/external"
	"geo-observers-blockchain/core/requests"
	"geo-observers-blockchain/core/responses"
	"geo-observers-blockchain/core/settings"
	log "github.com/sirupsen/logrus"
	"math"
	"time"
)

// todo: think about time synchronisation once in a week/month,
//       to prevent permanent delta increasing and time frames shifting.

const (
	kSynchronisationTimoutSeconds = 20

	// WARN!
	// Initial time frame index can't be 0, because it is valid index.
	// It also can't be < 0, because of uint16.
	// It is expected, that observers count would never exceed uint16.
	// So it seems, that ideal initial value for kInitialTimeFrameIndex should be MAX uint16.
	// On the next increment it would be set to 0.
	kInitialTimeFrameIndex = math.MaxUint16
)

// ToDo: synchronisation mechanics needs huge testing.
//       It works well enough for beta and internal GEO Client development, and even for the test-net,
//       but IT IS NOT READY for the production usage.

type Timer struct {
	OutgoingEventsTimeFrameEnd chan *EventTimeFrameEnd
	OutgoingRequestsTimeFrames chan *requests.RequestSynchronisationTimeFrames
	IncomingRequestsTimeFrames chan *requests.RequestSynchronisationTimeFrames
	OutgoingResponsesTimeFrame chan *responses.ResponseTimeFrame
	IncomingResponsesTimeFrame chan *responses.ResponseTimeFrame

	settings *settings.Settings

	// Internal events bus is used for controlling internal events loop.
	// For example, in case if synchronisation with external observers is finished,
	// and ticker might be started.
	internalEventsBus chan interface{}

	// External observers configuration reporter.
	confReporter *external.Reporter

	// Time left for the next time frame.
	// By default, it is equal to the block generation time duration,
	// but might be set to lover value after Sync() call.
	nextFrameTimestamp time.Time

	// Time when synchronisation must be finished.
	synchronisationDeadlineTimestamp time.Time

	// If true - then timer is synchronized and is generating new ticks.
	// By default is set to "false", because it is expected,
	// that timer would be synchronized first.
	isTickerRunning bool

	// Frame == current time window.
	// Total frames count == total observers count in current observers configuration.
	// Each one frame is related to the corresponding observer.
	// Frames are used as logical time windows for observers to emit blocks.
	// For example, for observer 0 it's related time frame has number 0.
	// Current frame index monotonically increases over time.
	// In case of current frame index reaches last observer -
	// process begins from the beginning.
	frame *EventTimeFrameEnd
}

func New(settings *settings.Settings, reporter *external.Reporter) *Timer {
	initialConfiguration, _ := reporter.GetCurrentConfiguration()

	return &Timer{
		// Outgoing events channel is not buffered.
		// It is better to lost timer tick, than process several ticks
		// one by one without any delay, that might be considered as, malicious behaviour.
		OutgoingEventsTimeFrameEnd: make(chan *EventTimeFrameEnd),
		OutgoingRequestsTimeFrames: make(chan *requests.RequestSynchronisationTimeFrames, 1),
		OutgoingResponsesTimeFrame: make(chan *responses.ResponseTimeFrame, 1),

		// On synchronization stage,
		// timer should be able to collect up to MAX OBSERVERS count of responses.
		IncomingResponsesTimeFrame: make(chan *responses.ResponseTimeFrame, common.ObserversMaxCount),
		IncomingRequestsTimeFrames: make(chan *requests.RequestSynchronisationTimeFrames, 1),

		// Internal events bus is used to control and to interrupt internal events loop.
		internalEventsBus: make(chan interface{}),

		settings: settings,

		confReporter: reporter,

		frame: &EventTimeFrameEnd{
			Index: kInitialTimeFrameIndex,
			Conf:  initialConfiguration,
		},
	}
}

func (t *Timer) Run(errors chan error) {
	shortLoop := func() {
		select {

		// todo: reconfigure frames on external observers configuration change

		case timeFramesRequest := <-t.IncomingRequestsTimeFrames:
			{
				err := t.processTimeFrameRequest(timeFramesRequest)
				common.SendErrorIfAny(err, errors)
			}

		case event := <-t.internalEventsBus:
			{
				err := t.processInternalEvent(event)
				common.SendErrorIfAny(err, errors)
			}
		}
	}

	fullLoop := func() {
		select {

		// todo: reconfigure frames on external observers configuration change

		case _ = <-time.After(t.nextFrameTimeLeft()):
			{
				t.processTick()
			}

		case timeFramesRequest := <-t.IncomingRequestsTimeFrames:
			{
				err := t.processTimeFrameRequest(timeFramesRequest)
				common.SendErrorIfAny(err, errors)
			}

		case event := <-t.internalEventsBus:
			{
				err := t.processInternalEvent(event)
				common.SendErrorIfAny(err, errors)
			}
		}
	}

	// WARN!
	// Whole synchronisation flow MUST perform faster than one block generation timeout.
	// At the moment, current logic does not support time frames synchronisation
	// when 2 or more blocks was generated during synchronisation.
	//
	// todo: add support of short blocks timeouts.
	var (
		kMinimalTimeFramesExchangeTimeoutSeconds = 20
		kMinimalAppropriateTimeoutSeconds        = int(common.AverageBlockGenerationTimeRange.Seconds()) -
			kMinimalTimeFramesExchangeTimeoutSeconds
	)

	if kSynchronisationTimoutSeconds >= kMinimalAppropriateTimeoutSeconds {
		panic(ErrInvalidSynchronisationTimeout)
	}

	// Attempt to sync with other observers before any operations processing.
	// It is asynchronous operation, so it must be launched in goroutine
	// to not block internal events loop and make it possible to respond to requests from core.
	// Static assert check.
	go t.syncWithOtherObservers()

	for {
		if t.isTickerRunning {
			fullLoop()

		} else {
			shortLoop()
		}
	}
}

func (t *Timer) syncWithOtherObservers() {

	t.synchronisationDeadlineTimestamp = time.Now().Add(time.Second * kSynchronisationTimoutSeconds)

	setNextTick := func(offset time.Duration) {
		t.nextFrameTimestamp = time.Now().Add(offset)

		// Interrupt internal loop, so this change would be processed.
		t.internalEventsBus <- &EventTickerStarted{}
	}

	collectResponses := func() {
		for {
			if time.Now().After(t.synchronisationDeadlineTimestamp) {
				break
			}

			time.Sleep(time.Millisecond * 50)
			if len(t.IncomingResponsesTimeFrame) == common.ObserversMaxCount {
				// There is no reason to wait longer.
				// All responses has been collected.
				break
			}
		}
	}

	// Request external observers for their current time frames data.
	// Timer would process all collected responses and
	// would adjust it's own configuration in accordance to the majority.
	t.OutgoingRequestsTimeFrames <- &requests.RequestSynchronisationTimeFrames{}

	t.log().Info("Time frames synchronization started...")
	collectResponses()

	nextFrameOffset, nextFrameIndex, responsesCollected, err := t.processMajorityOfFrameResponses()
	if err == common.ErrEmptySequence {
		t.log().Debug(
			"Time frames synchronisation is done. " +
				"NO RESPONSES was received and took into account.")

		// Use default block generation time range.
		t.frame = &EventTimeFrameEnd{Index: nextFrameIndex}
		setNextTick(common.AverageBlockGenerationTimeRange)

	} else {
		t.log().Debug(
			"Time frames synchronisation is done, ",
			responsesCollected, " respond(s) was took into account.")

		t.frame = &EventTimeFrameEnd{Index: nextFrameIndex}
		setNextTick(time.Nanosecond * time.Duration(nextFrameOffset))
	}
}

func (t *Timer) processInternalEvent(event interface{}) error {
	switch event.(type) {
	case *EventTickerStarted:
		{
			t.isTickerRunning = true
			return nil
		}

	default:
		return common.ErrNilParameter
	}
}

// processTimeFrameRequest schedules sending of response
// with information about CURRENT time frame index and amount of nanoseconds to it's change.
// In case if timer is in sync mode - it also adds amount of nanoseconds to the sync. finish.
func (t *Timer) processTimeFrameRequest(request *requests.RequestSynchronisationTimeFrames) error {

	var response *responses.ResponseTimeFrame

	if !t.isTickerRunning {
		if t.synchronisationDeadlineTimestamp.Second() == 0 {
			// In case if timer is stopped, but is not in synchronisation phase -
			// no time frame response should be returned, but it's also not an error.
			return nil
		}
	}

	kNextFrameTimeLeft := common.AverageBlockGenerationTimeRange.Nanoseconds() +
		t.synchronisationDeadlineTimestamp.Sub(time.Now()).Nanoseconds()

	if t.frame.Index == kInitialTimeFrameIndex {
		response = responses.NewResponseTimeFrame(
			request,
			0,
			uint64(kNextFrameTimeLeft))

	} else {
		response = responses.NewResponseTimeFrame(
			request,
			t.frame.Index,
			uint64(kNextFrameTimeLeft))
	}

	select {
	case t.OutgoingResponsesTimeFrame <- response:
		return nil

	default:
		return common.ErrChannelTransferringFailed
	}
}

func (t *Timer) processTick() {
	nextFrameNumber := t.frame.Index + 1
	if nextFrameNumber == common.ObserversMaxCount {
		nextFrameNumber = 0
	}

	// Warn!
	// New event always must replace previous one.
	// Do not update event's fields directly!
	t.frame = &EventTimeFrameEnd{
		Index: nextFrameNumber,
		Conf:  t.frame.Conf,
	}

	select {
	case t.OutgoingEventsTimeFrameEnd <- t.frame:
		{
		}
	default:
	}
}

// nextFrameTimeLeft returns time duration to the next time frame.
// Might be called several times during frame processing:
// each time the result would be les than the previous,
// so it is ok for events to interrupt internal events loop.
func (t *Timer) nextFrameTimeLeft() (d time.Duration) {
	timeLeft := t.nextFrameTimestamp.Sub(time.Now())
	if timeLeft <= 0 {
		t.nextFrameTimestamp = time.Now().Add(
			common.AverageBlockGenerationTimeRange).Add(
			timeLeft * time.Nanosecond * -1)

		return t.nextFrameTimeLeft()
	}

	return timeLeft
}

func (t *Timer) reconfigureFrames(e *external.EventConfigurationChanged) {
	// todo: implement on the ethereum connection implementation stage
}

// processMajorityAndCalculateAverageNextFrameTTL returns average time offset.
// Returns 0 in case if no offset is present if offsets.
func (t *Timer) processMajorityAndCalculateAverageNextFrameTTL(majorityOfTimeOffsets []uint64) uint64 {
	if len(majorityOfTimeOffsets) == 0 {
		return 0
	}

	// todo: Accept only majority of votes, (median, consensus)
	//       (K%, K == consensus count).

	var total uint64 = 0
	for _, ttl := range majorityOfTimeOffsets {
		total += ttl
	}

	return total / uint64(len(majorityOfTimeOffsets))
}

// processMajorityOfFrameResponses processes collected time frames responses,
// finds the majority of the responses, checks if majority has reached the consensus,
// and collects time offsets of the observers, that has fit into the majority.
// Returns error in case if consensus has not been reached.
func (t *Timer) processMajorityOfFrameResponses() (
	timeOffsetNanoseconds uint64, nextFrameIndex uint16, collectedResponsesCount uint16, err error) {
	collectedResponsesCount = uint16(len(t.IncomingResponsesTimeFrame))
	if collectedResponsesCount == 0 {
		return 0, 0, 0, common.ErrEmptySequence
	}

	rates := make(map[uint16]*[]uint64)

	var (
		i                  uint16
		topFrameIndex      uint16
		topFrameVotesCount = 0
		currentTTLsCount   = 0
		now                = time.Now()
	)

	for i = 0; i < collectedResponsesCount; i++ {
		vote := <-t.IncomingResponsesTimeFrame
		frameIndex := vote.FrameIndex

		TTLs, isPresent := rates[frameIndex]

		// todo: add comment
		timeOffset := now.Sub(vote.Received).Nanoseconds()

		var correctedNanosecondsLeft int64 = 0
		correctedNanosecondsLeft = int64(common.AverageBlockGenerationTimeRange) +
			int64(vote.NanosecondsLeft) -
			int64(timeOffset)

		if isPresent {
			*TTLs = append(*TTLs, uint64(correctedNanosecondsLeft))
			currentTTLsCount = len(*TTLs)

		} else {
			rates[frameIndex] = &[]uint64{uint64(correctedNanosecondsLeft)}
			currentTTLsCount = 1
		}

		if currentTTLsCount > topFrameVotesCount {
			topFrameIndex = frameIndex
			topFrameVotesCount = currentTTLsCount
		}
	}

	m, _ := rates[topFrameIndex]
	timeOffsetNanoseconds = t.processMajorityAndCalculateAverageNextFrameTTL(*m)

	frameOffset := 0
	if timeOffsetNanoseconds > uint64(common.AverageBlockGenerationTimeRange.Nanoseconds()) {
		frameOffset = 1
	}

	nextFrameIndex = topFrameIndex + uint16(frameOffset)
	if nextFrameIndex >= common.ObserversMaxCount {
		nextFrameIndex = 0
	}

	return
}

func (t *Timer) log() *log.Entry {
	return log.WithFields(log.Fields{"subsystem": "timer"})
}