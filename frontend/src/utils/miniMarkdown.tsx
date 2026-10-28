import React from 'react';

// 只认模型回答里常见的几样：标题、列表、代码块、粗体、行内代码。
// 不引第三方库，也不用 innerHTML，模型吐出什么字都只会当文字显示。

const renderInline = (text: string, keyPrefix: string): React.ReactNode[] => {
    const nodes: React.ReactNode[] = [];
    const pattern = /(\*\*[^*]+\*\*|`[^`]+`)/g;
    let last = 0;
    let match: RegExpExecArray | null;
    let index = 0;
    while ((match = pattern.exec(text)) !== null) {
        if (match.index > last) nodes.push(text.slice(last, match.index));
        const token = match[0];
        const key = `${keyPrefix}-${index++}`;
        nodes.push(token.startsWith('**')
            ? <strong key={key}>{token.slice(2, -2)}</strong>
            : <code key={key}>{token.slice(1, -1)}</code>);
        last = match.index + token.length;
    }
    if (last < text.length) nodes.push(text.slice(last));
    return nodes;
};

type Block =
    | { kind: 'p'; lines: string[] }
    | { kind: 'h'; level: number; text: string }
    | { kind: 'ul' | 'ol'; items: string[] }
    | { kind: 'code'; text: string };

const parseBlocks = (source: string): Block[] => {
    const blocks: Block[] = [];
    const lines = source.replace(/\r\n/g, '\n').split('\n');
    for (let i = 0; i < lines.length; i++) {
        const line = lines[i];
        const trimmed = line.trim();
        const previous = blocks[blocks.length - 1];
        if (trimmed.startsWith('```')) {
            const body: string[] = [];
            for (i++; i < lines.length && !lines[i].trim().startsWith('```'); i++) body.push(lines[i]);
            blocks.push({ kind: 'code', text: body.join('\n') });
            continue;
        }
        if (!trimmed) {
            blocks.push({ kind: 'p', lines: [] }); // 空行断开段落和列表
            continue;
        }
        const heading = /^(#{1,4})\s+(.*)$/.exec(trimmed);
        if (heading) {
            blocks.push({ kind: 'h', level: heading[1].length, text: heading[2] });
            continue;
        }
        const bullet = /^[-*•]\s+(.*)$/.exec(trimmed);
        const ordered = /^\d+[.)、]\s*(.*)$/.exec(trimmed);
        if (bullet || ordered) {
            const kind = bullet ? 'ul' : 'ol';
            const text = (bullet || ordered)![1];
            if (previous && previous.kind === kind) previous.items.push(text);
            else blocks.push({ kind, items: [text] });
            continue;
        }
        if (previous && previous.kind === 'p' && previous.lines.length > 0) previous.lines.push(trimmed);
        else blocks.push({ kind: 'p', lines: [trimmed] });
    }
    return blocks.filter((block) => block.kind !== 'p' || block.lines.length > 0);
};

const MiniMarkdown: React.FC<{ text: string; className?: string }> = ({ text, className }) => (
    <div className={className}>
        {parseBlocks(text).map((block, index) => {
            const key = `b${index}`;
            switch (block.kind) {
                case 'h':
                    return <div key={key} className={`md-h md-h${block.level}`}>{renderInline(block.text, key)}</div>;
                case 'ul':
                case 'ol': {
                    const List = block.kind;
                    return (
                        <List key={key}>
                            {block.items.map((item, itemIndex) => (
                                <li key={itemIndex}>{renderInline(item, `${key}-${itemIndex}`)}</li>
                            ))}
                        </List>
                    );
                }
                case 'code':
                    return <pre key={key}>{block.text}</pre>;
                default:
                    return (
                        <p key={key}>
                            {block.lines.map((line, lineIndex) => (
                                <React.Fragment key={lineIndex}>
                                    {lineIndex > 0 && <br />}
                                    {renderInline(line, `${key}-${lineIndex}`)}
                                </React.Fragment>
                            ))}
                        </p>
                    );
            }
        })}
    </div>
);

export default MiniMarkdown;
