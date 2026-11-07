// The connection of the agent tab to the app host of its agent. The host
// runs Pi on the computer that runs Galpon. `galpon herdr-app app <agent>`
// starts it when it does not run and tells its port and token.
//
// On a screen (another computer that shows Galpon, see `galpon connect`),
// 127.0.0.1 of the host is not reachable from here: the mod sends each
// request with curl on the host through herdr-core, and curl reads the token
// from the headers file of the host.

import { appHome, exec } from "herdr";
import * as fs from "fs/promises";
import { Buffer } from "buffer";

const GALPON = __GALPON__;
// HOST is the herdr-core machine name of the computer that runs Galpon, or
// null when Galpon runs on this computer.
export const HOST = __HOST__;
// The header of the token of the app host.
const TOKEN_HEADER = "X-Galpon-App-Token";
// One command line has a size limit. A bigger request goes in parts.
const CHUNK = 30000;

const message = (error) => String(error?.message ?? error);

// galpon runs `galpon herdr-app <args>` on the computer that runs Galpon and
// returns its JSON result.
export async function galpon(args) {
  const result = HOST
    ? await exec(GALPON, ["herdr-app", "--screen", ...args], { on: HOST })
    : await exec(GALPON, ["herdr-app", ...args]);
  const text = (result.stdout ?? "").trim();
  if (!text) throw new Error((result.stderr ?? "").trim() || `galpon exited with code ${result.code}`);
  return JSON.parse(text.split("\n").pop());
}

export class Host {
  constructor(machine = HOST) {
    this.machine = machine;
    this.port = 0;
    this.token = "";
    this.headers = "";
    this.mode = machine ? "curl" : "fetch";
    this.counter = 0;
  }

  set(info) {
    this.port = info.port;
    this.token = info.token;
    this.headers = info.headers ?? "";
  }

  async request(method, path, body, seconds = 30) {
    const json = body === undefined ? undefined : JSON.stringify(body);
    const url = `http://127.0.0.1:${this.port}${path}`;
    if (this.mode === "fetch") {
      try {
        const response = await fetch(url, { method, headers: { [TOKEN_HEADER]: this.token, "Content-Type": "application/json" }, body: json });
        const text = await response.text();
        return { status: response.status, body: text ? JSON.parse(text) : null };
      } catch (error) {
        // A mod without a network grant uses curl through exec.
        if (!/not granted/i.test(message(error))) throw error;
        this.mode = "curl";
      }
    }
    if (json !== undefined && this.machine && json.length > CHUNK) return this.parts(path, json, seconds);
    return this.curl(method, url, json, seconds);
  }

  async curl(method, url, json, seconds) {
    const argv = ["-sS", "-m", String(seconds), "-X", method, "-H", this.headers ? `@${this.headers}` : `${TOKEN_HEADER}: ${this.token}`, "-H", "Content-Type: application/json", "-w", "\n%{http_code}"];
    let file = "";
    if (json !== undefined) {
      if (this.machine) {
        // The text goes as it is: --data-raw does not read "@file".
        argv.push("--data-raw", json);
      } else {
        // A file keeps the prompt out of the process list and out of the argument size limit.
        try {
          await fs.mkdir(`${appHome()}/data/galpon-agent/requests`, { recursive: true });
          file = `${appHome()}/data/galpon-agent/requests/${Date.now()}-${++this.counter}.json`;
          await fs.writeFile(file, json);
          argv.push("--data-binary", `@${file}`);
        } catch {
          file = "";
          argv.push("--data-raw", json);
        }
      }
    }
    argv.push(url);
    try {
      const out = await exec("curl", argv, { on: this.machine });
      if (out.code !== 0) throw new Error(out.stderr.trim() || `curl exited with code ${out.code}`);
      const at = out.stdout.lastIndexOf("\n");
      const text = out.stdout.slice(0, at);
      return { status: Number(out.stdout.slice(at + 1)), body: text ? JSON.parse(text) : null };
    } finally {
      if (file) fs.unlink(file).catch(() => {});
    }
  }

  // parts sends a big body in parts. The host joins them and answers the last.
  async parts(path, json, seconds) {
    const id = `u${Date.now().toString(36)}${Math.random().toString(36).slice(2, 8)}`;
    const count = Math.ceil(json.length / CHUNK);
    let last = null;
    for (let index = 0; index < count; index++) {
      const part = json.slice(index * CHUNK, (index + 1) * CHUNK);
      const query = `/chunk?id=${id}&index=${index}&count=${count}&to=${encodeURIComponent(path)}`;
      last = await this.curl("POST", `http://127.0.0.1:${this.port}${query}`, part, seconds);
      if (last.status >= 400) return last;
    }
    return last;
  }

  // copyImage copies an image of the session from the host to this
  // computer, at the same path under the app home, so that image() draws it.
  async copyImage(path) {
    const response = await this.request("GET", `/image?path=${encodeURIComponent(path)}`, undefined, 60);
    if (response.status !== 200 || typeof response.body?.data !== "string") throw new Error(response.body?.error ?? "no image");
    const file = `${appHome()}/${path}`;
    await fs.mkdir(file.slice(0, file.lastIndexOf("/")), { recursive: true });
    await fs.writeFile(file, Buffer.from(response.body.data, "base64"));
  }
}

// clipboardImage reads an image from the clipboard of this computer as
// base64, for a host on another computer.
export async function clipboardImage() {
  const types = await exec("sh", ["-c", "wl-paste --list-types 2>/dev/null"]);
  const type = types.stdout.split("\n").map((line) => line.trim()).find((line) => /^image\/(png|jpeg|gif|webp)$/.test(line));
  if (!type) return null;
  const out = await exec("sh", ["-c", 'wl-paste --no-newline --type "$1" | base64 -w0', "sh", type]);
  if (out.code !== 0 || !out.stdout) return null;
  return { mimeType: type, data: out.stdout.trim() };
}
