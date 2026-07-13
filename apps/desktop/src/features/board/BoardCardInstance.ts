export type BoardCardInstance = Readonly<{
  columnID: string;
  taskID: string;
}>;

export type BoardCardInstanceKey = string;

export function boardCardInstanceKey(instance: BoardCardInstance): BoardCardInstanceKey {
  return JSON.stringify([instance.columnID, instance.taskID]);
}
