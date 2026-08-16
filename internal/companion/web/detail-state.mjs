function mirroredResponseIDs(detail) {
  return new Set((detail?.mirroredDeliveryResponses || []).map((id) => `delivery:${id}:response`));
}

function combinedMirroredDeliveries(first, second) {
  return [...new Set([
    ...(first?.mirroredDeliveryResponses || []),
    ...(second?.mirroredDeliveryResponses || []),
  ])];
}

export function mergeRefreshedDetail(previous, fresh) {
  if (!previous || previous.agent.id !== fresh.agent.id) return fresh;
  const mirroredResponses = mirroredResponseIDs(fresh);
  const mirroredDeliveryResponses = combinedMirroredDeliveries(previous, fresh);
  const freshIDs = new Set(fresh.timeline.map((event) => String(event?.eventId || "")));
  const previousRealIDs = previous.timeline
    .filter((event) => Number(event?.seq || 0) > 0)
    .map((event) => String(event?.eventId || ""));
  const freshRealIDs = fresh.timeline
    .filter((event) => Number(event?.seq || 0) > 0)
    .map((event) => String(event?.eventId || ""));
  const previousPromptIDs = previous.timeline
    .map((event) => String(event?.eventId || ""))
    .filter((id) => id.startsWith("delivery:") && id.endsWith(":prompt"));
  const freshPromptIDs = new Set(fresh.timeline
    .map((event) => String(event?.eventId || ""))
    .filter((id) => id.startsWith("delivery:") && id.endsWith(":prompt")));
  const bothHaveRealEvents = previousRealIDs.length && freshRealIDs.length;
  const realRangeGap = bothHaveRealEvents && !previousRealIDs.some((id) => freshIDs.has(id));
  const promptOverlap = previousPromptIDs.length && freshPromptIDs.size
    && previousPromptIDs.some((id) => freshPromptIDs.has(id));
  const messageRangeGap = !bothHaveRealEvents && previous.timeline.length && fresh.timeline.length && !promptOverlap;
  const emptyFreshRange = previous.timeline.length && !fresh.timeline.length;
  if (realRangeGap || messageRangeGap || emptyFreshRange) {
    return { ...fresh, mirroredDeliveryResponses };
  }
  const older = previous.timeline.filter((event) => {
    const id = String(event?.eventId || "");
    if (freshIDs.has(id) || mirroredResponses.has(id)) return false;
    const sequence = Number(event?.seq || 0);
    return sequence === 0 || fresh.before > 0 && sequence < fresh.before;
  });
  if (!older.length) return { ...fresh, mirroredDeliveryResponses };
  return {
    ...fresh,
    timeline: [...older, ...fresh.timeline],
    hasMore: previous.hasMore,
    before: previous.before,
    messageBefore: previous.messageBefore,
    mirroredDeliveryResponses,
  };
}

export function mergeOlderDetail(current, older) {
  const mirroredResponses = mirroredResponseIDs(older);
  const base = current.timeline.filter((event) => !mirroredResponses.has(String(event?.eventId || "")));
  const seen = new Set(base.map((event) => String(event?.eventId || "")));
  return {
    ...current,
    timeline: [...older.timeline.filter((event) => !seen.has(String(event?.eventId || ""))), ...base],
    hasMore: older.hasMore,
    before: older.before,
    messageBefore: older.messageBefore,
    mirroredDeliveryResponses: combinedMirroredDeliveries(current, older),
  };
}
