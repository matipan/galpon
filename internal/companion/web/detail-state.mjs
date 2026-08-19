function mirroredResponseIDs(detail) {
  return new Set((detail?.mirroredDeliveryResponses || []).map((id) => `delivery:${id}:response`));
}

function combinedMirroredDeliveries(first, second) {
  return [...new Set([
    ...(first?.mirroredDeliveryResponses || []),
    ...(second?.mirroredDeliveryResponses || []),
  ])];
}

function stableTimelineOrder(events, local = []) {
  const values = Array.isArray(events) ? events : [];
  return [
    ...values.filter((event) => Number(event?.seq || 0) > 0),
    ...values.filter((event) => Number(event?.seq || 0) <= 0),
    ...local,
  ];
}

function realEventIDs(detail) {
  return (detail?.timeline || [])
    .filter((event) => Number(event?.seq || 0) > 0)
    .map((event) => String(event?.eventId || ""));
}

export function mergeRefreshedDetail(previous, fresh) {
  if (!previous || previous.agent.id !== fresh.agent.id) return fresh;
  const mirroredResponses = mirroredResponseIDs(fresh);
  const mirroredDeliveryResponses = combinedMirroredDeliveries(previous, fresh);
  const freshIDs = new Set(fresh.timeline.map((event) => String(event?.eventId || "")));
  const previousRealIDs = realEventIDs(previous);
  const freshRealIDs = new Set(realEventIDs(fresh));
  const previousMessagePageIDs = previous.messagePageIds || [];
  const freshMessagePageIDs = new Set(fresh.messagePageIds || []);
  const preserveRealRange = previousRealIDs.length > 0 && freshRealIDs.size > 0
    && previousRealIDs.some((id) => freshRealIDs.has(id));
  const preserveMessageRange = previousMessagePageIDs.length > 0 && freshMessagePageIDs.size > 0
    && previousMessagePageIDs.some((id) => freshMessagePageIDs.has(id));

  const local = previous.timeline.filter((event) => {
    const id = String(event?.eventId || "");
    return event?.localOnly === true && !freshIDs.has(id);
  });
  const older = previous.timeline.filter((event) => {
    const id = String(event?.eventId || "");
    if (event?.localOnly === true || freshIDs.has(id) || mirroredResponses.has(id)) return false;
    const sequence = Number(event?.seq || 0);
    if (sequence > 0) return preserveRealRange && fresh.before > 0 && sequence < fresh.before;
    return preserveMessageRange;
  });
  return {
    ...fresh,
    timeline: stableTimelineOrder([...older, ...fresh.timeline], local),
    conversationHasMore: preserveRealRange ? previous.conversationHasMore : fresh.conversationHasMore,
    before: preserveRealRange ? previous.before : fresh.before,
    messageHasMore: preserveMessageRange ? previous.messageHasMore : fresh.messageHasMore,
    messageBefore: preserveMessageRange ? previous.messageBefore : fresh.messageBefore,
    hasMore: (preserveRealRange ? previous.conversationHasMore : fresh.conversationHasMore)
      || (preserveMessageRange ? previous.messageHasMore : fresh.messageHasMore),
    mirroredDeliveryResponses,
  };
}

export function mergeOlderDetail(current, older) {
  const mirroredResponses = mirroredResponseIDs(older);
  const base = current.timeline.filter((event) => !mirroredResponses.has(String(event?.eventId || "")));
  const seen = new Set(base.map((event) => String(event?.eventId || "")));
  const conversationRequested = current.conversationHasMore && current.before > 0;
  const messageRequested = current.messageHasMore && Boolean(current.messageBefore);
  const conversationHasMore = conversationRequested ? older.conversationHasMore : current.conversationHasMore;
  const messageHasMore = messageRequested ? older.messageHasMore : current.messageHasMore;
  return {
    ...current,
    timeline: stableTimelineOrder([...older.timeline.filter((event) => !seen.has(String(event?.eventId || ""))), ...base]),
    conversationHasMore,
    before: conversationRequested ? older.before : current.before,
    messageHasMore,
    messageBefore: messageRequested ? older.messageBefore : current.messageBefore,
    hasMore: conversationHasMore || messageHasMore,
    mirroredDeliveryResponses: combinedMirroredDeliveries(current, older),
  };
}
