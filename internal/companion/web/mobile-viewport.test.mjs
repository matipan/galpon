import test from "node:test";
import assert from "node:assert/strict";
import { desktopMobileScale } from "./mobile-viewport.mjs";

test("normal mobile layout needs no compensation", () => {
  assert.equal(desktopMobileScale({
    innerWidth: 384,
    screenWidth: 384,
    screenHeight: 854,
    coarsePointer: true,
  }), 1);
});

test("desktop-site mode on a phone compensates its fitted layout viewport", () => {
  assert.equal(desktopMobileScale({
    innerWidth: 980,
    screenWidth: 385,
    screenHeight: 833,
    coarsePointer: true,
  }), 980 / 385);
});

test("desktop-site landscape mode uses the current screen width", () => {
  assert.equal(desktopMobileScale({
    innerWidth: 980,
    screenWidth: 833,
    screenHeight: 385,
    coarsePointer: true,
  }), 980 / 833);
});

test("desktop and non-touch layouts are not changed", () => {
  assert.equal(desktopMobileScale({
    innerWidth: 980,
    screenWidth: 385,
    screenHeight: 833,
    coarsePointer: false,
  }), 1);
  assert.equal(desktopMobileScale({
    innerWidth: 1440,
    screenWidth: 1440,
    screenHeight: 900,
    coarsePointer: true,
  }), 1);
});
