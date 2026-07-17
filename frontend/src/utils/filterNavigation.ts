export type FilterReturnSource = {
    view: string;
    media: unknown | null;
} | null;

export const getFilterReturnLabel = (source: FilterReturnSource) => {
    if (!source) {
        return '';
    }
    if (source.media) {
        return '返回详情';
    }
    if (source.view === 'actor') {
        return '返回演员';
    }
    if (source.view === 'genre') {
        return '返回类别';
    }
    return '返回';
};

export const getTopBarBackLabel = (source: FilterReturnSource, currentView: string) => {
    const sourceLabel = getFilterReturnLabel(source);
    if (sourceLabel) {
        return sourceLabel;
    }
    return currentView !== 'libs' && currentView !== 'settings' ? '返回主页' : '';
};
