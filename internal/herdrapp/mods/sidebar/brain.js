// The small brain of the barn animals. When an animal ends an action, it
// selects the next one from the state of its agent:
//
//   needs you       it hurries to the gate, looks at you and moos
//   ready for you   it jumps one time, walks to its bucket, lies down and
//                   chews, and looks at you from time to time
//   running         it pulls a plow from one end of its lane to the other,
//                   and stops to eat from time to time
//   idle            it sleeps; from time to time it wakes up, walks, eats,
//                   meets a neighbor, and lies down again
//   no status yet   it walks, eats and looks around
//
// The pointer on an animal stops it, and it looks at you.
//
// The brain only plans. The app moves an animal from one place to the next
// with a transition, so the barn draws again only when an action ends. A
// heavy cow walks slowly, and a calf walks fast and hops.

export const JUMP_UP = 170;
export const JUMP_DOWN = 210;

const between = (random, lo, hi) => lo + random() * (hi - lo);
const clamp = (value, lo, hi) => Math.min(hi, Math.max(lo, value));

export function createBrain(random = Math.random) {
  return { plans: new Map(), random };
}

// currentX gives where an animal is in its pen at the time now: on the way
// from fromX to x while it walks.
export function currentX(plan, now) {
  if (!plan.moveMs || now >= plan.moveStart + plan.moveMs) return plan.x;
  return plan.fromX + ((plan.x - plan.fromX) * Math.max(0, now - plan.moveStart)) / plan.moveMs;
}

function set(plan, action, now, ms, facing = null) {
  plan.action = action;
  plan.until = now + ms;
  if (facing) plan.facing = facing;
}

function walkTo(plan, animal, target, now, after = null, hurry = 1) {
  const from = currentX(plan, now);
  const distance = Math.abs(target - from);
  plan.fromX = from;
  plan.x = target;
  plan.moveStart = now;
  plan.moveMs = distance < 1 ? 0 : Math.round((distance / (animal.speed * hurry)) * 1000);
  if (distance >= 1) plan.facing = target > from ? 1 : -1;
  set(plan, "walk", now, plan.moveMs);
  plan.after = after;
}

// stop ends a walk where the animal is now.
function stop(plan, now) {
  const x = currentX(plan, now);
  plan.fromX = x;
  plan.x = x;
  plan.moveStart = now;
  plan.moveMs = 120;
}

function jump(plan, now, height) {
  plan.jumpAt = now;
  plan.jumpHeight = height;
}

// jumpPhase gives "up" or "down" during a jump, else "".
export function jumpPhase(plan, now) {
  if (!plan.jumpAt) return "";
  if (now < plan.jumpAt + JUMP_UP) return "up";
  return now < plan.jumpAt + JUMP_UP + JUMP_DOWN ? "down" : "";
}

const calf = (animal) => (animal.cost?.size.scale ?? 3) <= 2;

// planFor gives the plan of an animal, and makes it the first time. A new
// animal starts at the place of its state, without a walk across its pen.
export function planFor(brain, animal, now) {
  let plan = brain.plans.get(animal.key);
  if (plan) return plan;
  const random = brain.random;
  plan = {
    state: animal.state, x: animal.home, fromX: animal.home, moveStart: now, moveMs: 0, facing: 1,
    action: "stand", until: now, after: null, jumpAt: 0, jumpHeight: 0, phase: "", awake: 0, passes: 0,
    partner: null, heart: false, hover: false,
  };
  const at = (x) => {
    plan.x = x;
    plan.fromX = x;
  };
  switch (animal.state) {
    case "needs":
      at(animal.gate ?? animal.home);
      set(plan, "look", now, Infinity, -1);
      break;
    case "ready":
      at(animal.bucket ?? animal.home);
      set(plan, "lie", now, between(random, 20000, 50000));
      break;
    case "idle":
      set(plan, "sleep", now, between(random, 30000, 240000));
      break;
    case "running":
      at(animal.lane?.lo ?? animal.home);
      set(plan, "stand", now, 300);
      break;
    default:
      set(plan, "stand", now, between(random, 500, 3000));
  }
  brain.plans.set(animal.key, plan);
  return plan;
}

// hover stops an animal under the pointer, and it looks at you. When the
// pointer leaves, it goes on with what its state asks.
export function hover(brain, key, on, now) {
  const plan = brain.plans.get(key);
  if (!plan || plan.hover === on) return false;
  plan.hover = on;
  if (on) {
    if (plan.action === "walk") stop(plan, now);
    plan.after = null;
    set(plan, "look", now, Infinity);
  } else {
    plan.until = now + 700;
  }
  return true;
}

// think runs the brain at the time now. It gives whether something changed,
// so that the barn draws again, and when it must run next.
export function think(brain, animals, now) {
  let changed = false;
  let next = now + 5000;
  const present = new Set(animals.map((animal) => animal.key));
  for (const key of brain.plans.keys()) {
    if (!present.has(key)) brain.plans.delete(key);
  }
  for (const animal of animals) {
    const plan = planFor(brain, animal, now);
    // A pen that changed its size keeps the animal inside.
    const lane = animal.state === "running" && animal.lane ? animal.lane : animal.pen;
    if (lane && plan.action !== "walk" && (plan.x < lane.lo - 1 || plan.x > lane.hi + 1)) {
      plan.x = clamp(plan.x, lane.lo, lane.hi);
      plan.fromX = plan.x;
      plan.moveMs = 0;
      changed = true;
    }
    if (plan.state !== animal.state) {
      changeState(brain, plan, animal, now);
      changed = true;
    }
    if (!plan.hover && now >= plan.until) {
      nextAction(brain, plan, animal, now, animals);
      changed = true;
    }
    const phase = jumpPhase(plan, now);
    if (phase !== plan.phase) {
      plan.phase = phase;
      if (!phase) plan.jumpAt = 0;
      changed = true;
    }
    if (plan.jumpAt) next = Math.min(next, phase === "up" ? plan.jumpAt + JUMP_UP : plan.jumpAt + JUMP_UP + JUMP_DOWN);
    if (!plan.hover) next = Math.min(next, plan.until);
  }
  return { changed, next: Math.max(now + 40, next) };
}

function changeState(brain, plan, animal, now) {
  const before = plan.state;
  plan.state = animal.state;
  plan.after = null;
  plan.partner = null;
  plan.heart = false;
  plan.awake = 0;
  plan.passes = 0;
  if (plan.hover) return;
  switch (animal.state) {
    case "needs":
      walkTo(plan, animal, animal.gate ?? animal.home, now, { action: "look", ms: Infinity, facing: -1 }, 1.8);
      return;
    case "ready":
      // An agent that finishes makes its animal jump, and then it walks to
      // its bucket.
      if (plan.action === "walk") stop(plan, now);
      if (before !== "idle") jump(plan, now, 12);
      set(plan, "stand", now, before !== "idle" ? JUMP_UP + JUMP_DOWN + 250 : 200);
      return;
    case "running":
      if (plan.action === "walk") stop(plan, now);
      set(plan, "stand", now, 400);
      return;
    case "idle":
      if (plan.action === "walk") stop(plan, now);
      set(plan, "lie", now, 1500);
      return;
    default:
      set(plan, "stand", now, 600);
  }
}

function nextAction(brain, plan, animal, now, animals) {
  const random = brain.random;
  if (plan.action === "walk" && plan.after) {
    const after = plan.after;
    plan.after = null;
    set(plan, after.action, now, after.ms, after.facing ?? null);
    if (after.action === "nose") plan.heart = Boolean(after.heart);
    return;
  }
  plan.heart = false;
  plan.partner = null;
  switch (animal.state) {
    case "needs": {
      const gate = animal.gate ?? animal.home;
      if (Math.abs(currentX(plan, now) - gate) > 2) walkTo(plan, animal, gate, now, { action: "look", ms: Infinity, facing: -1 }, 1.8);
      else set(plan, "look", now, Infinity, -1);
      return;
    }
    case "ready": {
      const spot = animal.bucket ?? animal.home;
      if (Math.abs(currentX(plan, now) - spot) > 2) {
        walkTo(plan, animal, spot, now, { action: "lie", ms: between(random, 25000, 60000), facing: 1 });
      } else if (plan.action === "lie") {
        set(plan, "look", now, between(random, 2500, 4000));
      } else {
        set(plan, "lie", now, between(random, 25000, 60000), 1);
      }
      return;
    }
    case "running": {
      const lane = animal.lane ?? animal.pen;
      if (plan.action === "walk") {
        plan.passes++;
        if (plan.passes >= 3 && random() < 0.4) {
          plan.passes = 0;
          set(plan, "eat", now, between(random, 2500, 4000));
        } else {
          set(plan, "stand", now, between(random, 500, 1100));
        }
        return;
      }
      const x = currentX(plan, now);
      walkTo(plan, animal, Math.abs(x - lane.hi) < Math.abs(x - lane.lo) ? lane.lo : lane.hi, now);
      return;
    }
    case "idle": {
      if (plan.action === "sleep") {
        plan.awake = 1 + Math.floor(random() * 3);
        set(plan, "stand", now, 1500);
        return;
      }
      if (plan.awake > 0) {
        plan.awake--;
        wander(brain, plan, animal, now, animals);
        return;
      }
      if (plan.action !== "lie") set(plan, "lie", now, 2000);
      else set(plan, "sleep", now, between(random, 120000, 420000));
      return;
    }
    default:
      wander(brain, plan, animal, now, animals);
  }
}

// free tells whether an animal can meet a neighbor now.
function free(plan, animal) {
  if (plan.hover || plan.partner || plan.action === "walk") return false;
  if (animal.state === "idle") return plan.awake > 0 && plan.action !== "sleep" && plan.action !== "lie";
  return animal.state === "pending";
}

// wander makes an animal walk to a place in its pen, eat or look around,
// and sometimes meet a neighbor nose to nose.
function wander(brain, plan, animal, now, animals) {
  const random = brain.random;
  if (random() < 0.2 && meet(brain, plan, animal, now, animals)) return;
  if (plan.action === "walk") {
    set(plan, random() < 0.7 ? "eat" : "look", now, between(random, 2500, 7000));
    return;
  }
  const lane = animal.pen;
  walkTo(plan, animal, lane.lo + random() * (lane.hi - lane.lo), now);
  if (calf(animal) && random() < 0.5) jump(plan, now, 6);
}

// meet sends an animal and a free neighbor of its pen to touch noses. The
// one on the left looks to the right, and the other one to the left.
function meet(brain, plan, animal, now, animals) {
  const x = currentX(plan, now);
  const neighbor = animals.find((other) => {
    if (other.key === animal.key || other.penId !== animal.penId) return false;
    const otherPlan = brain.plans.get(other.key);
    return otherPlan && free(otherPlan, other) && Math.abs(currentX(otherPlan, now) - x) > Math.max(animal.w, other.w);
  });
  if (!neighbor) return false;
  const other = brain.plans.get(neighbor.key);
  const [left, leftPlan, right, rightPlan] = x < currentX(other, now) ? [animal, plan, neighbor, other] : [neighbor, other, animal, plan];
  const middle = (currentX(leftPlan, now) + left.w + currentX(rightPlan, now)) / 2;
  const ms = between(brain.random, 2200, 3200);
  walkTo(leftPlan, left, clamp(middle - left.w + 2, left.pen.lo, left.pen.hi), now, { action: "nose", ms, facing: 1, heart: true });
  walkTo(rightPlan, right, clamp(middle - 2, right.pen.lo, right.pen.hi), now, { action: "nose", ms, facing: -1 });
  leftPlan.partner = right.key;
  rightPlan.partner = left.key;
  return true;
}

// poseOf gives how to draw an animal at the time now: its place in its pen,
// how long the app takes to move it there, where it looks, its sprite and
// what goes with it.
export function poseOf(plan, animal, now) {
  const cow = animal.family === "cow";
  let sprite = animal.kind;
  if (plan.action === "eat") sprite += "Eat";
  if (plan.action === "lie") sprite += "Lie";
  if (plan.action === "sleep") sprite += "Sleep";
  if (plan.action === "look" && cow) sprite += "Front";
  const walking = plan.action === "walk" && now < plan.moveStart + plan.moveMs;
  const phase = jumpPhase(plan, now);
  const heavy = (animal.cost?.total ?? 0) >= 1000;
  return {
    x: plan.x,
    moveMs: plan.moveMs,
    facing: plan.facing,
    sprite,
    front: plan.action === "look" && cow,
    walking,
    jumpY: phase === "up" ? -plan.jumpHeight : 0,
    jumpMs: phase === "up" ? JUMP_UP : JUMP_DOWN,
    jumpEase: phase === "up" ? "ease-out" : "ease-in",
    plow: animal.state === "running",
    moo: animal.state === "needs" && plan.action === "look",
    chew: animal.state === "ready" && plan.action === "lie",
    sleeping: plan.action === "sleep",
    heart: plan.action === "nose" && plan.heart,
    coins: animal.state === "running" ? (animal.cost?.coins ?? 0) : 0,
    bird: plan.action === "sleep" && animal.changedAt !== null && now - animal.changedAt > 30 * 60 * 1000,
    puff: walking && heavy,
  };
}
