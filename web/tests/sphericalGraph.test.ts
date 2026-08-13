import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  distributeOnSphere,
  selectPersistentLabelIds,
  truncateGraphLabel,
} from "../src/lib/sphericalGraph.ts";

const closeTo = (actual: number, expected: number): void => {
  assert.ok(Math.abs(actual - expected) < 1e-9, `${actual} is not close to ${expected}`);
};

describe("spherical graph geometry", () => {
  it("returns no points for an empty graph", () => {
    assert.deepEqual(distributeOnSphere([]), {});
  });

  it("puts a single table at the front of the sphere", () => {
    assert.deepEqual(distributeOnSphere(["orders"]), {
      orders: { x: 0, y: 0, z: 1 },
    });
  });

  it("places every key deterministically on a unit sphere without changing the input", () => {
    const keys = ["orders", "customers", "regions"] as const;

    const first = distributeOnSphere(keys);
    const second = distributeOnSphere(keys);

    assert.deepEqual(first, second);
    assert.deepEqual(keys, ["orders", "customers", "regions"]);
    assert.deepEqual(Object.keys(first), keys);
    for (const point of Object.values(first)) {
      closeTo(Math.hypot(point.x, point.y, point.z), 1);
    }
  });
});

describe("spherical graph labels", () => {
  it("keeps short labels unchanged", () => {
    assert.equal(truncateGraphLabel("sales.orders"), "sales.orders");
  });

  it("bounds long labels without splitting Unicode code points", () => {
    assert.equal(truncateGraphLabel("orders-🚀-archive", 10), "orders-🚀-…");
    assert.equal(truncateGraphLabel("orders", 0), "");
  });

  it("caps persistent labels by degree with a deterministic name tie-break", () => {
    const candidates = [
      { id: "orders", name: "orders", degree: 4 },
      { id: "regions", name: "regions", degree: 1 },
      { id: "accounts", name: "accounts", degree: 4 },
    ] as const;

    assert.deepEqual([...selectPersistentLabelIds(candidates, 2)], ["accounts", "orders"]);
    assert.deepEqual(candidates.map((candidate) => candidate.id), ["orders", "regions", "accounts"]);
  });
});
