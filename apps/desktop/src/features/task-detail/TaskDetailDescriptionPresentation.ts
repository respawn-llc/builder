export type DescriptionPresentationState = Readonly<{
  editing: boolean;
  expanded: boolean;
}>;

export const initialDescriptionPresentationState: DescriptionPresentationState = {
  editing: false,
  expanded: false,
};
