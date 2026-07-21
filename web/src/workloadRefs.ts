import { SecretMount, Volume } from "./api";

export interface MountEntry { service: string; secretName: string; target: string; mode: string }

export function mountsToEntries(mounts?: SecretMount[]): MountEntry[] {
  return (mounts ?? []).map((m) => ({
    service: m.service,
    secretName: m.secret_name,
    target: m.target ?? "",
    mode: m.mode ? m.mode.toString(8) : "",
  }));
}

export function entriesToMounts(entries: MountEntry[]): SecretMount[] | undefined {
  const mounts: SecretMount[] = [];
  for (const e of entries) {
    if (!e.service || !e.secretName) continue;
    const mount: SecretMount = { service: e.service, secret_name: e.secretName };
    if (e.target) mount.target = e.target;
    if (e.mode) {
      const parsed = parseInt(e.mode, 8);
      if (!isNaN(parsed)) mount.mode = parsed;
    }
    mounts.push(mount);
  }
  return mounts.length ? mounts : undefined;
}

// validateMountEntries flags rows that are half-filled (only one of
// service/secretName set) so the caller can block save instead of having
// entriesToMounts silently drop the row.
export function validateMountEntries(entries: MountEntry[]): string | null {
  for (const e of entries) {
    if (e.secretName && !e.service) {
      return `Secret mount for "${e.secretName}" is missing a service name`;
    }
    if (e.service && !e.secretName) {
      return `Secret mount for service "${e.service}" is missing a secret name`;
    }
  }
  return null;
}

export interface SecretVolumeEntry { secretName: string; target: string; mode: string }

// volumesToSecretEntries extracts the secret-typed volumes from a container
// template's Volumes list for editing. Non-secret (bind/volume) entries are
// excluded here — callers must preserve those separately and pass them back
// through entriesToSecretVolumes' `preserved` argument.
export function volumesToSecretEntries(volumes?: Volume[]): SecretVolumeEntry[] {
  return (volumes ?? [])
    .filter((v) => v.type === "secret")
    .map((v) => ({
      secretName: v.source,
      target: v.target ?? "",
      mode: v.mode ? v.mode.toString(8) : "",
    }));
}

// entriesToSecretVolumes converts edited secret-volume rows back into Volume
// entries (type: "secret", source: secret name), merged with any `preserved`
// non-secret volumes so plain bind/volume mounts are never dropped just
// because the secret-mount rows were edited.
export function entriesToSecretVolumes(entries: SecretVolumeEntry[], preserved: Volume[] = []): Volume[] | undefined {
  const vols: Volume[] = [...preserved];
  for (const e of entries) {
    if (!e.secretName) continue;
    const vol: Volume = { type: "secret", source: e.secretName };
    if (e.target) vol.target = e.target;
    if (e.mode) {
      const parsed = parseInt(e.mode, 8);
      if (!isNaN(parsed)) vol.mode = parsed;
    }
    vols.push(vol);
  }
  return vols.length ? vols : undefined;
}

export interface RefEntry { envVar: string; name: string }

export function refsToEntries(refs?: Record<string, string>): RefEntry[] {
  return Object.entries(refs ?? {}).map(([envVar, name]) => ({ envVar, name }));
}

export function entriesToRefs(entries: RefEntry[]): Record<string, string> | undefined {
  const refs: Record<string, string> = {};
  for (const e of entries) {
    if (e.envVar) refs[e.envVar] = e.name;
  }
  return Object.keys(refs).length ? refs : undefined;
}
