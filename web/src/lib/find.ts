export const fileTypes = ['video', 'image', 'audio', 'document', 'archive', 'other'] as const;
export type FileType = (typeof fileTypes)[number];

export type FindRun = {
  id: string;
  profileId: string;
  directoryId: string;
  directOnly: boolean;
  types: FileType[];
  query?: string;
};

export function readFind(): FindRun | null {
  try {
    const value: unknown = JSON.parse(sessionStorage.getItem('http-wrapper.find') ?? 'null');
    if (!value || typeof value !== 'object') return null;
    const run = value as Partial<FindRun>;
    if (
      typeof run.id !== 'string' ||
      typeof run.profileId !== 'string' ||
      typeof run.directoryId !== 'string' ||
      typeof run.directOnly !== 'boolean' ||
      (run.query !== undefined && (typeof run.query !== 'string' || run.query.length > 512)) ||
      !Array.isArray(run.types) ||
      !run.types.every((type) => fileTypes.includes(type))
    ) return null;
    return run as FindRun;
  } catch {
    return null;
  }
}
