export type LibraryDeleteOutcome = {
    deleted?: boolean;
    warning?: string;
};

export const applyLibraryDeleteOutcome = (
    result: LibraryDeleteOutcome | null | undefined,
    onDeleted: () => void,
    onWarning: (warning: string) => void,
): boolean => {
    if (!result?.deleted) {
        return false;
    }
    onDeleted();
    if (result.warning) {
        onWarning(result.warning);
    }
    return true;
};
