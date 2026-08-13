export interface Point3D {
  x: number;
  y: number;
  z: number;
}

export interface LabelCandidate {
  id: string;
  name: string;
  degree: number;
}

const GOLDEN_ANGLE = Math.PI * (3 - Math.sqrt(5));

/** Bounds text that becomes a GPU-backed canvas label; tooltips retain the full name. */
export function truncateGraphLabel(text: string, maximumCharacters = 28): string {
  if (maximumCharacters <= 0) return "";
  const characters = Array.from(text);
  if (characters.length <= maximumCharacters) return text;
  return `${characters.slice(0, Math.max(0, maximumCharacters - 1)).join("")}…`;
}

/** Selects a stable, bounded set of useful labels without changing the input. */
export function selectPersistentLabelIds(
  candidates: readonly LabelCandidate[],
  maximumLabels = 80,
): ReadonlySet<string> {
  return new Set(
    [...candidates]
      .sort((first, second) => second.degree - first.degree || first.name.localeCompare(second.name))
      .slice(0, Math.max(0, maximumLabels))
      .map((candidate) => candidate.id),
  );
}

/** Places stable keys evenly over a unit sphere using a Fibonacci lattice. */
export function distributeOnSphere(keys: readonly string[]): Record<string, Point3D> {
  if (keys.length === 1) {
    return { [keys[0]]: { x: 0, y: 0, z: 1 } };
  }

  return Object.fromEntries(
    keys.map((key, index) => {
      const y = 1 - (2 * index) / Math.max(1, keys.length - 1);
      const ringRadius = Math.sqrt(Math.max(0, 1 - y * y));
      const angle = index * GOLDEN_ANGLE;
      return [
        key,
        {
          x: Math.cos(angle) * ringRadius,
          y,
          z: Math.sin(angle) * ringRadius,
        },
      ];
    }),
  );
}
