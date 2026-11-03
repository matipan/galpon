#!/usr/bin/env python3
"""Makes the barn sounds of the Galpon layout: a moo, a rooster and a cluck.

The sounds are synthesized, so they have no license of another person. Each
voice is a sum of harmonics of a pitch curve, shaped by the resonances of a
mouth, as a voice is. Run it in this folder; it needs lame for the MP3 files:

    python3 synth.py

Galpon embeds the MP3 files, and `galpon herdr-app install` copies them to
~/.config/herdr-app/sounds.
"""
import math
import os
import random
import struct
import subprocess
import tempfile
import wave

RATE = 44100


def resonance(frequency, center, width):
    return 1.0 / (1.0 + ((frequency - center) / width) ** 2)


def voice(duration, pitch, formants, loudness, roughness=0.0, noise=0.0, seed=1, top=7000, floor=0.02):
    """Synthesizes a voice. pitch(t) and loudness(t) give the curves, and
    formants(t) gives the (center, width, gain) of each resonance."""
    rng = random.Random(seed)
    samples = []
    phase = 0.0
    jitter = 0.0
    for index in range(int(duration * RATE)):
        t = index / RATE
        jitter = jitter * 0.995 + rng.uniform(-1.0, 1.0) * roughness
        f0 = max(40.0, pitch(t) + jitter)
        phase += f0 / RATE
        shape = formants(t)
        value = 0.0
        harmonic = 1
        while harmonic * f0 < top:
            frequency = harmonic * f0
            gain = sum(g * resonance(frequency, c, w) for c, w, g in shape) + floor
            value += gain / harmonic ** 0.7 * math.sin(2 * math.pi * harmonic * phase)
            harmonic += 1
        if noise:
            value += rng.uniform(-1.0, 1.0) * noise
        samples.append(value * loudness(t))
    return samples


def envelope(t, start, end, attack, release):
    if t < start or t > end:
        return 0.0
    return min(1.0, (t - start) / attack, (end - t) / release)


def moo():
    """Mmm-ooo-uh: a low voice that opens and closes its mouth."""
    duration = 1.45

    def pitch(t):
        if t < 0.28:
            f = 112 + 38 * (t / 0.28) ** 0.6
        elif t < 0.95:
            f = 150 - 10 * (t - 0.28) / 0.67
        else:
            f = 140 - 42 * (t - 0.95) / 0.5
        return f + 2.0 * math.sin(2 * math.pi * 5.2 * t)

    def formants(t):
        if t < 0.14:
            mouth = 0.0
        elif t < 0.45:
            mouth = (t - 0.14) / 0.31
        elif t < 0.98:
            mouth = 1.0
        else:
            mouth = max(0.0, 1.0 - (t - 0.98) / 0.47)
        return [(250 + 330 * mouth, 80 + 40 * mouth, 1.0), (650 + 420 * mouth, 150, 0.55), (2400, 300, 0.08 * mouth)]

    return voice(duration, pitch, formants, lambda t: envelope(t, 0, duration, 0.09, 0.3), roughness=0.7, seed=7, top=3200, floor=0.004)


def rooster():
    """Cock-a-doodle-doo: five syllables, the last long and shaky."""
    syllables = [
        (0.00, 0.10, 560, 720),
        (0.13, 0.07, 650, 620),
        (0.23, 0.12, 700, 790),
        (0.38, 0.11, 760, 830),
        (0.52, 0.78, 840, 640),
    ]
    duration = 1.36

    def current(t):
        for start, length, low, high in syllables:
            if start <= t <= start + length:
                return start, length, low, high
        return None

    def pitch(t):
        part = current(t)
        if not part:
            return 700.0
        start, length, low, high = part
        x = (t - start) / length
        if length > 0.5:
            # Up first, then a slow fall with a shake.
            f = low + 90 * math.sin(math.pi * min(1.0, x * 2.2)) if x < 0.45 else low + 60 - (low + 60 - high) * (x - 0.45) / 0.55
            return f + 18 * math.sin(2 * math.pi * 8.5 * t)
        return low + (high - low) * x

    def formants(t):
        return [(1100, 260, 1.0), (2100, 420, 0.8), (3300, 600, 0.35)]

    def loudness(t):
        part = current(t)
        if not part:
            return 0.0
        start, length, _, _ = part
        gain = 1.0 if length > 0.5 else 0.8
        return gain * envelope(t, start, start + length, 0.015, 0.05 if length < 0.5 else 0.25)

    return voice(duration, pitch, formants, loudness, roughness=2.5, noise=0.04, seed=3, top=9000)


def cluck():
    """Buk, buk, bawk: two short clucks and a longer one."""
    calls = [(0.00, 0.055, 520, 410), (0.11, 0.055, 500, 400), (0.24, 0.17, 640, 520)]
    duration = 0.44

    def current(t):
        for start, length, high, low in calls:
            if start <= t <= start + length:
                return start, length, high, low
        return None

    def pitch(t):
        part = current(t)
        if not part:
            return 450.0
        start, length, high, low = part
        x = (t - start) / length
        if length > 0.1:
            return low + (high - low) * math.sin(math.pi * min(1.0, x * 1.3))
        return high + (low - high) * x

    def formants(t):
        return [(900, 200, 1.0), (1700, 320, 0.6)]

    def loudness(t):
        part = current(t)
        if not part:
            return 0.0
        start, length, _, _ = part
        return envelope(t, start, start + length, 0.004, 0.03 if length < 0.1 else 0.08)

    return voice(duration, pitch, formants, loudness, roughness=3.0, noise=0.06, seed=11, top=8000)


def write_mp3(name, samples, peak=0.5):
    top = max(abs(value) for value in samples) or 1.0
    frames = b"".join(struct.pack("<h", int(value / top * peak * 32767)) for value in samples)
    with tempfile.TemporaryDirectory() as folder:
        path = os.path.join(folder, name + ".wav")
        with wave.open(path, "wb") as out:
            out.setnchannels(1)
            out.setsampwidth(2)
            out.setframerate(RATE)
            out.writeframes(frames)
        subprocess.run(["lame", "--quiet", "-m", "m", "-b", "64", "-q", "2", path, name + ".mp3"], check=True)


if __name__ == "__main__":
    write_mp3("galpon-moo", moo())
    write_mp3("galpon-rooster", rooster())
    write_mp3("galpon-cluck", cluck(), peak=0.45)
